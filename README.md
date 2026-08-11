# mcp-telegram

An MCP server that reaches Telegram as two separate identities — a user account over
MTProto and a bot over the Bot API — and lets each of them touch only the chats it was
given.

The point is where the decision lives. The access lists are the server's own
configuration, and a caller holds no Telegram credentials, so there is no way to reach a
chat except through this server, and no way through it except past the lists.

## The access model

Four lists, one per identity and kind of access:

| Variable | What it allows |
|---|---|
| `TELEGRAM_USER_READ_CHATS` | chats the account may read |
| `TELEGRAM_USER_WRITE_CHATS` | chats the account may write to |
| `TELEGRAM_BOT_READ_CHATS` | chats the bot may read |
| `TELEGRAM_BOT_WRITE_CHATS` | chats the bot may write to |

- Write implies read. Writing blind to a chat is not a thing anyone wants.
- Chats are named by identifier, never by title. A title is renamed by whoever owns the
  chat, and a list that someone else can widen by renaming their group is not a list.
- The identifiers are the ones a Telegram client shows: positive for people, negative for
  groups, `-100…` for supergroups and channels.
- Writing is off entirely unless the server was started with `--allow-write`, and even
  then a writing tool is only registered for an identity whose write list is not empty.

The `telegram_access_lists` tool answers what is allowed without calling Telegram, so a
caller can find out before trying, and a refusal is not mistaken for a fault.

## Identities

`--identities` is required and takes `user`, `bot`, or `user,bot`. Nothing is guessed from
which credentials happen to be present: a server that decides for itself whose account it
acts as is a server nobody can reason about.

The two identities differ in what they can do, not in rank:

- The **account** sees every chat its person sees, and reads history as far back as it
  goes.
- The **bot** sees only the chats it was added to, and the Bot API keeps no history at
  all: a bot can read a message only while it is waiting in the update queue. There is no
  bot history tool for that reason.

## Tools

Read:

| Tool | What it does |
|---|---|
| `telegram_access_lists` | what this server may do, from configuration |
| `telegram_user_chats` | the chats the account may read, with titles |
| `telegram_user_chat_info` | one chat as the account sees it |
| `telegram_user_history` | a chat, oldest message first, the whole of it if asked |
| `telegram_user_search` | text search inside one chat |
| `telegram_user_download` | save a file attached to a message |
| `telegram_bot_info` | the bot itself, which also proves its token works |
| `telegram_bot_chat_info` | one chat as the bot sees it |
| `telegram_bot_updates` | the messages waiting for the bot |
| `telegram_bot_download` | save a file the bot can see |

Write, registered only with `--allow-write`:

| Tool | What it does |
|---|---|
| `telegram_user_send_message` | send text as the account |
| `telegram_user_send_file` | send a local file as the account |
| `telegram_bot_send_message` | send text as the bot |
| `telegram_bot_send_file` | send a local file as the bot |

Every message comes back with its identifier, date, author, text, the names and sizes of
its attachments, and what it replies to. A history read paginates for as long as it takes
and answers in reading order, so a year of one chat is one call.

The download tools appear only when `--download-dir` is set. Files land there under a name
built from the chat and message identifiers, and the name from Telegram is stripped to
its last element and to safe characters: a message must not be able to decide where on
disk this server writes.

## Configuration

Environment:

| Variable | Needed by |
|---|---|
| `TELEGRAM_API_ID`, `TELEGRAM_API_HASH` | the `user` identity |
| `TELEGRAM_BOT_TOKEN` | the `bot` identity |
| the four chat lists above | whatever the lists allow |
| `MCP_AUTH_TOKEN` | the HTTP transport, as a bearer token |

Flags:

| Flag | Default | What it does |
|---|---|---|
| `--identities` | none, required | `user`, `bot` or `user,bot` |
| `--allow-write` | off | register the writing tools |
| `--transport` | `stdio` | `stdio` or `streamable-http` |
| `--address` | `127.0.0.1:8815` | where to listen with the HTTP transport |
| `--session-dir` | none | where the MTProto session lives |
| `--download-dir` | none | where downloaded files go |
| `--version` | | print the version |

The server refuses to start rather than start half-configured: HTTP without
`MCP_AUTH_TOKEN`, `--allow-write` with two empty write lists, the `user` identity without
a session directory.

## Signing in

The account identity needs a session file, and only the person holding the phone can
create one: Telegram sends the code to them. Run the login command once, with the same
session directory the server will use.

```sh
export TELEGRAM_API_ID=… TELEGRAM_API_HASH=…
mcp-telegram login --session-dir ~/.local/share/mcp-telegram
```

It asks for the phone number, then the code, then the two-factor password if the account
has one. The password is read without echo.

One process holds one session. The file is a single store of connection keys, and a second
process opening it does not get a second connection, it gets a locked file. That is one of
the reasons this server is meant to run once and serve every client.

## Running it

Read-only over stdio:

```sh
mcp-telegram --identities=user,bot --session-dir ~/.local/share/mcp-telegram
```

Long-lived over HTTP, which is how it is meant to run — one process for every client on
the machine:

```sh
mcp-telegram --identities=user,bot \
  --transport=streamable-http --address=127.0.0.1:8815 \
  --session-dir ~/.local/share/mcp-telegram \
  --download-dir ~/Downloads/telegram
```

The HTTP transport serves `/mcp` behind a bearer token and `/healthz` without one. The
port reaches Telegram with this server's rights, and on loopback any local process can
knock, so the token is not optional and the comparison is constant-time.

The image is published to `ghcr.io/svesh87/mcp-telegram`, built for amd64 and arm64 from
one static binary in a `scratch` image: no shell inside, nothing to reach the session
with. It runs as uid 65532 by default; whoever mounts a session directory should override
that with their own identifier so the directory stays writable.

```sh
docker run --rm -i \
  -e TELEGRAM_API_ID -e TELEGRAM_API_HASH -e TELEGRAM_BOT_TOKEN \
  -e TELEGRAM_USER_READ_CHATS -e TELEGRAM_BOT_WRITE_CHATS \
  -e MCP_AUTH_TOKEN \
  --user "$(id -u):$(id -g)" \
  -v "$HOME/.local/share/mcp-telegram:/session" \
  -p 127.0.0.1:8815:8815 \
  ghcr.io/svesh87/mcp-telegram:latest \
  --identities=user,bot --transport=streamable-http --address=0.0.0.0:8815 \
  --session-dir=/session
```

## Building and gates

Go 1.25. No build tags, no code generation.

```sh
gofmt -l .
go vet ./...
go test ./... -coverprofile=coverage.out
```

The tests cover the access decisions, the message conversion, the pagination through
gotd's own iterators, and the Bot API against a local stand-in. Coverage is checked in CI
against a floor of 80%, and the build fails below it.

## What this is built on

- [gotd/td](https://github.com/gotd/td) for MTProto.
- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) for MCP over stdio and
  streamable HTTP.
- The Bot API is called directly over HTTP; it is a handful of methods and needs no
  library.
