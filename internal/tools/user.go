package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/svesh87/mcp-telegram/internal/access"
	"github.com/svesh87/mcp-telegram/internal/telegram"
)

// registerUser adds the tools that act as the account. Writing tools are added only when
// the server was started with writing enabled and the account has a write list, so a
// read-only server does not even advertise them.
func (r *registry) registerUser(srv *server.MCPServer) {
	srv.AddTool(mcp.NewTool("telegram_user_chats",
		mcp.WithDescription("List the chats this server may read as the account, with their titles. "+
			"Chats the account cannot see are reported separately, which is how a wrong identifier in "+
			"an access list shows up."),
		mcp.WithReadOnlyHintAnnotation(true),
	), r.userChats)

	srv.AddTool(mcp.NewTool("telegram_user_chat_info",
		mcp.WithDescription("Name one chat as the account sees it: kind, title, username."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithReadOnlyHintAnnotation(true),
	), r.userChatInfo)

	srv.AddTool(mcp.NewTool("telegram_user_history",
		mcp.WithDescription("Read a chat as the account, oldest message first. "+
			"Without limits it walks the whole history, paginating for as long as it takes, and every "+
			"message carries its identifier, date, author, text and the names of its attachments."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithNumber("limit", mcp.Description("Stop after this many messages. Omit to read everything.")),
		mcp.WithString("since", mcp.Description("Skip messages older than this date (2006-01-02 or RFC 3339).")),
		mcp.WithNumber("min_id", mcp.Description("Stop at this message identifier, to continue a previous read.")),
		mcp.WithNumber("offset_id", mcp.Description("Start below this message identifier, to walk further back.")),
		mcp.WithReadOnlyHintAnnotation(true),
	), r.userHistory)

	srv.AddTool(mcp.NewTool("telegram_user_search",
		mcp.WithDescription("Search text inside one chat as the account. "+
			"Search is per chat on purpose: a global search would reach chats that are in no access list."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithString("query", mcp.Required(), mcp.Description("Text to look for.")),
		mcp.WithNumber("limit", mcp.Description("Cap the number of matches.")),
		mcp.WithReadOnlyHintAnnotation(true),
	), r.userSearch)

	if r.opts.DownloadDir != "" {
		srv.AddTool(mcp.NewTool("telegram_user_download",
			mcp.WithDescription("Save the file attached to a message and answer with its path. "+
				"The file lands in the directory this server was configured with, under a name built "+
				"from the chat and message identifiers."),
			mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
			mcp.WithNumber("message_id", mcp.Required(), mcp.Description("Message carrying the file.")),
		), r.userDownload)
	}

	if !r.opts.writingAllowed(access.User) || r.opts.UserWrite == nil {
		return
	}

	srv.AddTool(mcp.NewTool("telegram_user_send_message",
		mcp.WithDescription("Send a message to a chat as the account. Only chats in the account's "+
			"write list are accepted."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message text.")),
		mcp.WithNumber("reply_to_message_id", mcp.Description("Answer this message.")),
	), r.userSendMessage)

	srv.AddTool(mcp.NewTool("telegram_user_send_file",
		mcp.WithDescription("Send a file to a chat as the account. "+fileHelp),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithString("path", mcp.Description(pathHelp)),
		mcp.WithString("file_name", mcp.Description("Name to send the file under. Required with content_base64.")),
		mcp.WithString("content_base64", mcp.Description("The file itself, base64 encoded.")),
		mcp.WithString("caption", mcp.Description("Text to send with the file.")),
	), r.userSendFile)
}

const chatIDHelp = "Chat identifier as a Telegram client shows it: negative for groups and channels, " +
	"positive for people. Names are not accepted."

const fileHelp = "Give the file either as a path on the machine running this server or as " +
	"content_base64 with a file_name, not both."

const pathHelp = "Path to the file on the machine running this server."

func (r *registry) userChats(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	visible, err := r.opts.UserRead.Chats(ctx)
	if err != nil {
		return toolError(err), nil
	}

	known := map[int64]telegram.Chat{}
	for _, chat := range visible {
		known[chat.ID] = chat
	}

	type entry struct {
		telegram.Chat
		Write bool `json:"write"`
	}

	payload := struct {
		Chats      []entry `json:"chats"`
		NotVisible []int64 `json:"not_visible,omitempty"`
	}{}

	// The dialog list is wider than the access lists, so it is filtered here rather than
	// handed over: a caller must not learn about chats it may not read.
	for _, chatID := range r.opts.Checker.Chats(access.User, access.Read) {
		chat, ok := known[chatID]
		if !ok {
			payload.NotVisible = append(payload.NotVisible, chatID)
			continue
		}

		payload.Chats = append(payload.Chats, entry{
			Chat:  chat,
			Write: r.opts.Checker.Allowed(access.User, access.Write, chatID),
		})
	}

	return mcp.NewToolResultJSON(payload)
}

func (r *registry) userChatInfo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.User, access.Read, chatID); err != nil {
		return toolError(err), nil
	}

	chat, err := r.opts.UserRead.ChatInfo(ctx, chatID)
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(chat)
}

func (r *registry) userHistory(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.User, access.Read, chatID); err != nil {
		return toolError(err), nil
	}

	since, err := sinceArg(req)
	if err != nil {
		return toolError(err), nil
	}

	messages, err := r.opts.UserRead.History(ctx, chatID, telegram.HistoryOptions{
		Limit:    req.GetInt("limit", 0),
		Since:    since,
		MinID:    req.GetInt("min_id", 0),
		OffsetID: req.GetInt("offset_id", 0),
	})
	if err != nil {
		return toolError(err), nil
	}

	return messagesResult(chatID, messages)
}

func (r *registry) userSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.User, access.Read, chatID); err != nil {
		return toolError(err), nil
	}

	query, err := req.RequireString("query")
	if err != nil {
		return toolError(err), nil
	}

	messages, err := r.opts.UserRead.Search(ctx, chatID, query, req.GetInt("limit", 0))
	if err != nil {
		return toolError(err), nil
	}

	return messagesResult(chatID, messages)
}

func (r *registry) userDownload(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.User, access.Read, chatID); err != nil {
		return toolError(err), nil
	}

	messageID, err := req.RequireInt("message_id")
	if err != nil {
		return toolError(err), nil
	}

	dir, err := r.downloadDir()
	if err != nil {
		return toolError(err), nil
	}

	saved, err := r.opts.UserRead.Download(ctx, chatID, messageID, dir)
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(saved)
}

func (r *registry) userSendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.User, access.Write, chatID); err != nil {
		return toolError(err), nil
	}

	text, err := req.RequireString("text")
	if err != nil {
		return toolError(err), nil
	}

	sent, err := r.opts.UserWrite.SendMessage(ctx, chatID, text, req.GetInt("reply_to_message_id", 0))
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(sent)
}

func (r *registry) userSendFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.User, access.Write, chatID); err != nil {
		return toolError(err), nil
	}

	file, err := fileArg(req)
	if err != nil {
		return toolError(err), nil
	}

	sent, err := r.opts.UserWrite.SendFile(ctx, chatID, file, req.GetString("caption", ""))
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(sent)
}

// messagesResult wraps a history or search answer. The count is there because a caller
// reading a year of one chat needs to know whether it got everything.
func messagesResult(chatID int64, messages []telegram.Message) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultJSON(struct {
		ChatID   int64              `json:"chat_id"`
		Count    int                `json:"count"`
		Messages []telegram.Message `json:"messages"`
	}{ChatID: chatID, Count: len(messages), Messages: messages})
}
