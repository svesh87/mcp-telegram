package transport

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/svesh87/mcp-telegram/internal/telegram"
)

// LoginPath is where the login page lives, under the public URL.
const LoginPath = "/login"

// maxLoginForm caps what a login form may send: a phone number, a code or a password.
const maxLoginForm = 16 << 10

// Web turns on the login page. It exists only when the server was given --public-url.
type Web struct {
	// PublicURL is the absolute address a browser reaches this server at, without a
	// trailing slash. Every link, form and redirect on the page is built from it, because
	// behind a proxy that strips a path prefix the request alone does not say where the
	// page is.
	PublicURL string
	// Login signs the user identity in. Nil when the user identity is not running.
	Login LoginService
}

// LoginService is the sign-in as the page sees it: telegram.WebLogin, or a stand-in in
// tests.
type LoginService interface {
	Account() telegram.Account
	Start(ctx context.Context, phone string) (telegram.LoginStep, error)
	Code(ctx context.Context, flow, code string) (telegram.LoginStep, error)
	Password(ctx context.Context, flow, password string) (telegram.LoginStep, error)
}

// loginPage serves the sign-in for the account owner.
//
// The bearer is checked in front of it, and it is never written into the page, a link or a
// form: in front of this server a gateway adds the header, and the browser never holds
// the token at all.
func loginPage(web *Web) http.Handler {
	action := web.PublicURL + LoginPath

	// A form posted from another site would ride on the owner's gateway session. Browsers
	// say where a request came from in Sec-Fetch-Site, and in Origin when they do not; the
	// public address is trusted as well as the Host header, because behind a gateway the two
	// differ. The check reads headers only, so a non-browser client is not affected.
	crossOrigin := http.NewCrossOriginProtection()
	if err := crossOrigin.AddTrustedOrigin(origin(web.PublicURL)); err != nil {
		panic("transport: public URL " + web.PublicURL + " has no usable origin: " + err.Error())
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; form-action 'self' "+origin(web.PublicURL)+
				"; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		view := loginView{Action: action, Stage: telegram.StagePhone, Enabled: web.Login != nil}

		switch r.Method {
		case http.MethodGet:

		case http.MethodPost:
			if err := crossOrigin.Check(r); err != nil {
				http.Error(w, "the form came from another site", http.StatusForbidden)
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxLoginForm)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "the form could not be read", http.StatusBadRequest)
				return
			}

			if web.Login == nil {
				break
			}

			step, err := submit(r, web.Login)
			if err != nil {
				view.Error = err.Error()
			} else {
				view.Stage, view.Flow, view.CodeKind, view.Notice = step.Stage, step.Flow, step.CodeKind, step.Notice
			}

		default:
			http.Error(w, "only GET and POST", http.StatusMethodNotAllowed)
			return
		}

		if web.Login != nil {
			view.Account = web.Login.Account()
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := loginTemplate.Execute(w, view); err != nil {
			// The response is already on its way; nothing useful is left to say to the
			// browser.
			return
		}
	})
}

// submit hands one form to the sign-in. Which form it was is said by the form itself.
func submit(r *http.Request, login LoginService) (telegram.LoginStep, error) {
	flow := r.PostFormValue("flow")

	switch r.PostFormValue("step") {
	case string(telegram.StageCode):
		return login.Code(r.Context(), flow, r.PostFormValue("code"))
	case string(telegram.StagePassword):
		return login.Password(r.Context(), flow, r.PostFormValue("password"))
	default:
		return login.Start(r.Context(), r.PostFormValue("phone"))
	}
}

// noStore keeps a page out of caches and its address out of referrers.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// origin is the scheme and host of an address, as a browser sends it in Origin.
func origin(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

type loginView struct {
	Action   string
	Enabled  bool
	Account  telegram.Account
	Stage    telegram.LoginStage
	Flow     string
	CodeKind string
	Notice   string
	Error    string
}

// The page is one file with no external anything: the server is built FROM scratch and
// the page must not depend on anything it cannot serve itself.
//
// Its text is in Russian while the rest of this repository is in English, and that split is
// deliberate: the page is read by one person, the account owner, while comments and error
// messages end up in agent transcripts and issues.
var loginTemplate = template.Must(template.New("login").Parse(strings.TrimSpace(`
<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Вход в Telegram</title>
<style>
:root { color-scheme: light dark; }
body {
  font: 16px/1.5 system-ui, sans-serif;
  max-width: 34rem; margin: 3rem auto; padding: 0 1rem;
}
h1 { font-size: 1.3rem; margin-bottom: .3rem; }
p.lead { color: GrayText; margin-top: 0; }
form { display: grid; gap: .8rem; margin: 1.5rem 0; }
label { display: grid; gap: .3rem; }
input {
  font: inherit; padding: .5rem .6rem;
  border: 1px solid GrayText; border-radius: .4rem; background: Field; color: FieldText;
}
button {
  font: inherit; padding: .6rem 1rem; border: 0; border-radius: .4rem;
  background: Highlight; color: HighlightText; cursor: pointer;
}
.box { border: 1px solid GrayText; border-radius: .5rem; padding: .8rem 1rem; margin: 1rem 0; }
.bad { border-color: #c0392b; }
.good { border-color: #2e7d32; }
</style>
</head>
<body>
<h1>Вход в Telegram</h1>
<p class="lead">
  Вход аккаунта, от имени которого сервер читает чаты. Номер, код и пароль уходят в Telegram
  и здесь не сохраняются и не пишутся в лог; сервер хранит только файл сессии.
</p>

{{ if not .Enabled }}
<div class="box">Аккаунт на этом сервере не включён (<code>--identities</code> без <code>user</code>): входить некому.</div>
{{ else }}

<div class="box">
{{ if .Account.SignedIn }}
  Вход выполнен{{ if .Account.Name }}: {{ .Account.Name }}{{ end }}{{ if .Account.Username }} (@{{ .Account.Username }}){{ end }}.
{{ else if .Account.CanSignIn }}
  Вход не выполнен.
{{ else }}
  Вход не выполнен. {{ .Account.Problem }}
{{ end }}
</div>

{{ if .Error }}<div class="box bad">{{ .Error }}</div>{{ end }}
{{ if .Notice }}<div class="box bad">{{ .Notice }}</div>{{ end }}

{{ if eq .Stage "done" }}
<div class="box good">
  Готово. Перезапускать сервер не нужно: инструменты аккаунта заработают, как только он
  загрузит список чатов, обычно за несколько секунд.
</div>
<p><a href="{{ .Action }}">Обновить</a></p>

{{ else if .Account.SignedIn }}
<p class="lead">
  Сессия работает. Чтобы войти другим аккаунтом, остановите сервер и уберите файл сессии.
</p>

{{ else if eq .Stage "code" }}
<p>Telegram прислал код {{ .CodeKind }}.</p>
<form method="post" action="{{ .Action }}" autocomplete="off">
  <input type="hidden" name="step" value="code">
  <input type="hidden" name="flow" value="{{ .Flow }}">
  <label>Код
    <input name="code" type="text" inputmode="numeric" autocomplete="one-time-code"
           maxlength="16" required autofocus>
  </label>
  <button type="submit">Дальше</button>
</form>
<p><a href="{{ .Action }}">Начать заново</a></p>

{{ else if eq .Stage "password" }}
<p>У аккаунта включена двухэтапная проверка.</p>
<form method="post" action="{{ .Action }}">
  <input type="hidden" name="step" value="password">
  <input type="hidden" name="flow" value="{{ .Flow }}">
  <label>Облачный пароль
    <input name="password" type="password" autocomplete="current-password" required autofocus>
  </label>
  <button type="submit">Войти</button>
</form>
<p><a href="{{ .Action }}">Начать заново</a></p>

{{ else if .Account.CanSignIn }}
<form method="post" action="{{ .Action }}">
  <input type="hidden" name="step" value="phone">
  <label>Номер телефона, с кодом страны
    <input name="phone" type="tel" autocomplete="tel" required autofocus>
  </label>
  <button type="submit">Прислать код</button>
</form>
<p class="lead">
  Ошибка в коде или пароле стоит одной попытки из трёх, а не всего входа. Попытка живёт
  десять минут.
</p>

{{ else }}
<p><a href="{{ .Action }}">Обновить</a></p>
{{ end }}

{{ end }}
</body>
</html>
`)))
