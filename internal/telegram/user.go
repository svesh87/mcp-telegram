package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

// SessionFile is the name of the MTProto session inside the session directory. One
// file, one process: it is a single file with the connection keys in it, and two
// processes sharing it fight over the same authorisation.
const SessionFile = "user.session"

// Page sizes. Telegram caps a single request well below the size of a real history, so
// every read here paginates.
const (
	historyBatch = 100
	dialogBatch  = 100
	searchLimit  = 100
)

// ErrNotAuthorized says the session file holds no authorisation. Only the owner of the
// phone number can fix it, so the message points at the command that does it.
var ErrNotAuthorized = errors.New(
	"this session is not authorized: run \"mcp-telegram login\" once with the same --session-dir")

// UserOptions configures the user identity.
type UserOptions struct {
	APIID      int
	APIHash    string
	SessionDir string
}

// UserClient reaches Telegram as the account behind the session file.
//
// One process holds one connection, and every caller shares it. That is not only
// tidier than a connection per caller: the session is a single SQLite-shaped file, and
// a second process opening it gets a locked database rather than a second connection.
type UserClient struct {
	client *telegram.Client
	raw    *tg.Client
	cache  *peerCache

	ready chan struct{}
	done  chan struct{}

	// refresh reloads the dialog list. It is a field so that the policy around it (once
	// at startup, again on an unknown identifier) can be tested without a connection.
	refresh func(ctx context.Context) error

	mu  sync.Mutex
	err error
}

// NewUser builds the client. It does not connect: Run does.
func NewUser(opts UserOptions) (*UserClient, error) {
	if opts.SessionDir == "" {
		return nil, errors.New("session directory is required for the user identity")
	}

	// 0700, because the file inside is an authorisation for the account.
	if err := os.MkdirAll(opts.SessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("session directory %s: %w", opts.SessionDir, err)
	}

	client := telegram.NewClient(opts.APIID, opts.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(opts.SessionDir, SessionFile)},
		// This server answers questions and sends what it is told to send. It does not
		// watch chats, so the update stream is dead weight.
		NoUpdates: true,
	})

	user := &UserClient{
		client: client,
		raw:    client.API(),
		cache:  newPeerCache(),
		ready:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	user.refresh = user.loadDialogs

	return user, nil
}

// Run connects and holds the connection until ctx ends. It returns the reason it
// stopped, and every caller waiting in Wait is released with the same reason.
func (u *UserClient) Run(ctx context.Context) (err error) {
	defer func() {
		u.mu.Lock()
		u.err = err
		u.mu.Unlock()
		close(u.done)
	}()

	return u.client.Run(ctx, func(ctx context.Context) error {
		status, err := u.client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("checking authorization: %w", err)
		}
		if !status.Authorized {
			return ErrNotAuthorized
		}

		// Chats are addressed by identifier, and MTProto needs an access hash with it.
		// The hashes arrive with the dialog list, so the list is fetched once at startup
		// and again whenever an identifier is not known.
		if err := u.refresh(ctx); err != nil {
			return fmt.Errorf("reading the dialog list: %w", err)
		}

		close(u.ready)

		<-ctx.Done()
		return ctx.Err()
	})
}

// Wait blocks until the client is usable, or until it is clear that it will not be.
func (u *UserClient) Wait(ctx context.Context) error {
	select {
	case <-u.ready:
		return nil
	case <-u.done:
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.err != nil {
			return u.err
		}
		return errors.New("the Telegram user client stopped")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *UserClient) loadDialogs(ctx context.Context) error {
	return query.GetDialogs(u.raw).BatchSize(dialogBatch).
		ForEach(ctx, func(ctx context.Context, elem dialogs.Elem) error {
			entities := elem.Entities
			u.cache.addUsers(usersOf(entities.Users()))
			u.cache.addChats(chatsOf(entities.Chats(), entities.Channels()))
			return nil
		})
}

// Chats lists what the account can see, as far as the dialog list goes. The access
// lists decide what a caller may do with them; this only answers what exists.
func (u *UserClient) Chats(ctx context.Context) ([]Chat, error) {
	if err := u.Wait(ctx); err != nil {
		return nil, err
	}

	u.cache.mu.RLock()
	chats := make([]Chat, 0, len(u.cache.chats))
	for _, chat := range u.cache.chats {
		chats = append(chats, chat)
	}
	u.cache.mu.RUnlock()

	sort.Slice(chats, func(i, j int) bool { return chats[i].ID < chats[j].ID })
	return chats, nil
}

// ChatInfo names one chat.
func (u *UserClient) ChatInfo(ctx context.Context, chatID int64) (Chat, error) {
	if err := u.Wait(ctx); err != nil {
		return Chat{}, err
	}

	if _, err := u.resolve(ctx, chatID); err != nil {
		return Chat{}, err
	}

	chat, ok := u.cache.chat(chatID)
	if !ok {
		return Chat{}, fmt.Errorf("chat %d is not known to this account", chatID)
	}

	return chat, nil
}

// resolve turns an access-list identifier into an input peer, refreshing the dialog
// list once if the identifier is new.
func (u *UserClient) resolve(ctx context.Context, chatID int64) (tg.InputPeerClass, error) {
	if peer, ok := u.cache.peer(chatID); ok {
		return peer, nil
	}

	if err := u.refresh(ctx); err != nil {
		return nil, fmt.Errorf("reading the dialog list: %w", err)
	}

	if peer, ok := u.cache.peer(chatID); ok {
		return peer, nil
	}

	return nil, fmt.Errorf("chat %d is not among this account's dialogs: "+
		"the account has to be a member of it, and %s says what the identifier refers to",
		chatID, ChatKindOf(chatID))
}

// HistoryOptions bounds a history read. All zero means the whole chat, oldest message
// first, which is the case this server was written for: a year of one chat, read in
// order.
type HistoryOptions struct {
	// Limit caps the number of messages. Zero means no cap.
	Limit int
	// Since drops messages older than this moment.
	Since time.Time
	// MinID stops at messages up to and including this identifier, for continuing a
	// previous read.
	MinID int
	// OffsetID starts below this identifier, for walking further back.
	OffsetID int
}

// History reads a chat, oldest message first.
func (u *UserClient) History(ctx context.Context, chatID int64, opts HistoryOptions) ([]Message, error) {
	if err := u.Wait(ctx); err != nil {
		return nil, err
	}

	peer, err := u.resolve(ctx, chatID)
	if err != nil {
		return nil, err
	}

	iterator := query.Messages(u.raw).GetHistory(peer).BatchSize(historyBatch).Iter()
	if opts.OffsetID > 0 {
		iterator = iterator.OffsetID(opts.OffsetID)
	}

	collected := &history{opts: opts}

	for iterator.Next(ctx) {
		elem := iterator.Value()

		raw, ok := elem.Msg.(tg.MessageClass)
		if !ok {
			continue
		}

		message, ok := ConvertMessage(raw, chatID, entitiesOf(elem.Entities))
		if !ok {
			continue
		}

		if collected.add(message) {
			break
		}
	}

	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("reading chat %d: %w", chatID, err)
	}

	return collected.result(), nil
}

// history collects a chat as it arrives and decides when to stop.
//
// Telegram hands history out newest first, and the bounds are checked here rather than
// passed in the request: min_id and offset_date cover only part of what a caller asks
// for, and stopping early costs one comparison per message.
type history struct {
	opts     HistoryOptions
	messages []Message
}

// add takes one message and reports whether the read is done.
func (h *history) add(message Message) bool {
	if h.opts.MinID > 0 && message.ID <= h.opts.MinID {
		return true
	}
	if !h.opts.Since.IsZero() && message.Date.Before(h.opts.Since) {
		return true
	}

	h.messages = append(h.messages, message)

	return h.opts.Limit > 0 && len(h.messages) >= h.opts.Limit
}

// result hands the messages over in reading order.
func (h *history) result() []Message {
	Reverse(h.messages)
	return h.messages
}

// downloadTarget decides what to download out of a message and where to put it.
func downloadTarget(raw tg.MessageClass, chatID int64, messageID int, dir string) (
	tg.InputFileLocationClass, string, int64, error,
) {
	message, ok := raw.(*tg.Message)
	if !ok || message.Media == nil {
		return nil, "", 0, fmt.Errorf("message %d of chat %d carries no file", messageID, chatID)
	}

	location, name, size, err := FileLocation(message.Media)
	if err != nil {
		return nil, "", 0, fmt.Errorf("message %d of chat %d: %w", messageID, chatID, err)
	}

	return location, filepath.Join(dir, DownloadName(chatID, messageID, name)), size, nil
}

// Search looks for text inside one chat. Search is per chat on purpose: a global
// search would reach chats that are in no access list.
func (u *UserClient) Search(ctx context.Context, chatID int64, text string, limit int) ([]Message, error) {
	if err := u.Wait(ctx); err != nil {
		return nil, err
	}

	if text == "" {
		return nil, errors.New("search needs a query: use the history tool to read a chat in order")
	}

	peer, err := u.resolve(ctx, chatID)
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > searchLimit {
		limit = searchLimit
	}

	result, err := u.raw.MessagesSearch(ctx, &tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      text,
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("searching chat %d: %w", chatID, err)
	}

	return searchResult(result, chatID)
}

// searchResult reads a search answer. Telegram can answer "nothing changed" to a
// request carrying a hash, and this one carries none, so that answer means something
// went wrong rather than that there are no matches.
func searchResult(result tg.MessagesMessagesClass, chatID int64) ([]Message, error) {
	modified, ok := result.AsModified()
	if !ok {
		return nil, fmt.Errorf("searching chat %d: Telegram answered with no results to read", chatID)
	}

	messages := ConvertMessages(modified.GetMessages(), chatID,
		NewEntities(modified.GetUsers(), modified.GetChats()))

	Reverse(messages)
	return messages, nil
}

// Download saves the file attached to a message and returns where it went.
func (u *UserClient) Download(ctx context.Context, chatID int64, messageID int, dir string) (SavedFile, error) {
	if err := u.Wait(ctx); err != nil {
		return SavedFile{}, err
	}

	peer, err := u.resolve(ctx, chatID)
	if err != nil {
		return SavedFile{}, err
	}

	raw, err := query.Messages(u.raw).GetMessages(ctx, peer, messageID)
	if err != nil {
		return SavedFile{}, fmt.Errorf("reading message %d of chat %d: %w", messageID, chatID, err)
	}
	if len(raw) == 0 {
		return SavedFile{}, fmt.Errorf("message %d is not in chat %d", messageID, chatID)
	}

	location, path, size, err := downloadTarget(raw[0], chatID, messageID, dir)
	if err != nil {
		return SavedFile{}, err
	}

	if _, err := u.client.Download(location).ToPath(ctx, path); err != nil {
		return SavedFile{}, fmt.Errorf("downloading the file of message %d: %w", messageID, err)
	}

	return SavedFile{Path: path, Size: size}, nil
}
