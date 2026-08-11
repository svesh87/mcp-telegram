package tools

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/svesh87/mcp-telegram/internal/access"
	"github.com/svesh87/mcp-telegram/internal/telegram"
)

// registerBot adds the tools that act as the bot.
//
// A bot reads differently from an account: the Bot API keeps no history, so the only
// messages it can read are the ones waiting in its update queue. There is no bot history
// tool for that reason, and its absence is not an omission.
func (r *registry) registerBot(srv *server.MCPServer) {
	srv.AddTool(mcp.NewTool("telegram_bot_info",
		mcp.WithDescription("Name the bot this server runs as, which is also the cheapest check "+
			"that its token still works."),
		mcp.WithReadOnlyHintAnnotation(true),
	), r.botInfo)

	srv.AddTool(mcp.NewTool("telegram_bot_chat_info",
		mcp.WithDescription("Name one chat as the bot sees it. The bot has to be a member of it."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithReadOnlyHintAnnotation(true),
	), r.botChatInfo)

	srv.AddTool(mcp.NewTool("telegram_bot_updates",
		mcp.WithDescription("Read the messages waiting for the bot. "+
			"Messages from chats outside the bot's read list are dropped and only counted. "+
			"Passing an offset acknowledges every update before it, and Telegram then forgets them, "+
			"so pass one only when the previous batch has been dealt with."),
		mcp.WithNumber("offset", mcp.Description("Acknowledge updates before this identifier and read from it on.")),
		mcp.WithNumber("limit", mcp.Description("Cap the number of updates.")),
	), r.botUpdates)

	if r.opts.DownloadDir != "" {
		srv.AddTool(mcp.NewTool("telegram_bot_download",
			mcp.WithDescription("Save a file the bot can see, addressed by the file identifier that "+
				"came with the message."),
			mcp.WithString("file_id", mcp.Required(), mcp.Description("File identifier from a message attachment.")),
		), r.botDownload)
	}

	if !r.opts.writingAllowed(access.Bot) || r.opts.BotWrite == nil {
		return
	}

	srv.AddTool(mcp.NewTool("telegram_bot_send_message",
		mcp.WithDescription("Send a message as the bot. Only chats in the bot's write list are accepted."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithString("text", mcp.Required(), mcp.Description("Message text.")),
		mcp.WithNumber("reply_to_message_id", mcp.Description("Answer this message.")),
	), r.botSendMessage)

	srv.AddTool(mcp.NewTool("telegram_bot_send_file",
		mcp.WithDescription("Send a local file as the bot."),
		mcp.WithString("chat_id", mcp.Required(), mcp.Description(chatIDHelp)),
		mcp.WithString("path", mcp.Required(), mcp.Description("Path to the file on the machine running this server.")),
		mcp.WithString("caption", mcp.Description("Text to send with the file.")),
	), r.botSendFile)
}

func (r *registry) botInfo(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	me, err := r.opts.BotRead.Me(ctx)
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(me)
}

func (r *registry) botChatInfo(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.Bot, access.Read, chatID); err != nil {
		return toolError(err), nil
	}

	chat, err := r.opts.BotRead.ChatInfo(ctx, chatID)
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(chat)
}

// botUpdates filters what the queue hands over. Anyone can write to a bot, so the queue
// is the one place where messages from chats in no access list arrive by themselves.
func (r *registry) botUpdates(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	messages, err := r.opts.BotRead.Updates(ctx, req.GetInt("offset", 0), req.GetInt("limit", 0))
	if err != nil {
		return toolError(err), nil
	}

	allowed := make([]telegram.Message, 0, len(messages))
	skipped := 0
	lastUpdateID := 0

	for _, message := range messages {
		if message.UpdateID > lastUpdateID {
			lastUpdateID = message.UpdateID
		}

		if !r.opts.Checker.Allowed(access.Bot, access.Read, message.ChatID) {
			skipped++
			continue
		}

		allowed = append(allowed, message)
	}

	return mcp.NewToolResultJSON(struct {
		Count int `json:"count"`
		// Skipped counts messages from chats the bot may not read. They are reported as a
		// number rather than dropped in silence, so an empty answer can be told apart
		// from an answer that was filtered.
		Skipped int `json:"skipped_not_in_access_list"`
		// NextOffset acknowledges everything in this batch, filtered messages included.
		NextOffset int                `json:"next_offset,omitempty"`
		Messages   []telegram.Message `json:"messages"`
	}{
		Count:      len(allowed),
		Skipped:    skipped,
		NextOffset: nextOffset(lastUpdateID),
		Messages:   allowed,
	})
}

func nextOffset(lastUpdateID int) int {
	if lastUpdateID == 0 {
		return 0
	}

	return lastUpdateID + 1
}

func (r *registry) botDownload(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	fileID, err := req.RequireString("file_id")
	if err != nil {
		return toolError(err), nil
	}

	dir, err := r.downloadDir()
	if err != nil {
		return toolError(err), nil
	}

	saved, err := r.opts.BotRead.Download(ctx, fileID, dir)
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(saved)
}

func (r *registry) botSendMessage(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.Bot, access.Write, chatID); err != nil {
		return toolError(err), nil
	}

	text, err := req.RequireString("text")
	if err != nil {
		return toolError(err), nil
	}

	sent, err := r.opts.BotWrite.SendMessage(ctx, chatID, text, req.GetInt("reply_to_message_id", 0))
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(sent)
}

func (r *registry) botSendFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	chatID, err := chatIDArg(req)
	if err != nil {
		return toolError(err), nil
	}

	if err := r.opts.Checker.Check(access.Bot, access.Write, chatID); err != nil {
		return toolError(err), nil
	}

	path, err := req.RequireString("path")
	if err != nil {
		return toolError(err), nil
	}

	sent, err := r.opts.BotWrite.SendFile(ctx, chatID, path, req.GetString("caption", ""))
	if err != nil {
		return toolError(err), nil
	}

	return mcp.NewToolResultJSON(sent)
}
