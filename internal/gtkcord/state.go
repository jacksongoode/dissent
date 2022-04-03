package gtkcord

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"

	"github.com/diamondburned/arikawa/v3/api"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/arikawa/v3/gateway"
	"github.com/diamondburned/arikawa/v3/state"
	"github.com/diamondburned/arikawa/v3/utils/httputil/httpdriver"
	"github.com/diamondburned/arikawa/v3/utils/ws"
	"github.com/diamondburned/chatkit/components/author"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotkit/gtkutil"
	"github.com/diamondburned/ningen/v3"
	"github.com/diamondburned/ningen/v3/discordmd"
)

func init() {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "PC"
	}

	api.UserAgent = "gtkcord4 (https://github.com/diamondburned/arikawa/v3)"
	gateway.DefaultIdentity = gateway.IdentifyProperties{
		OS:      runtime.GOOS,
		Device:  hostname,
		Browser: "gtkcord4",
	}
}

type ctxKey uint8

const (
	_ ctxKey = iota
	stateKey
)

// State extends the Discord state controller.
type State struct {
	*ningen.State
}

// FromContext gets the Discord state controller from the given context.
func FromContext(ctx context.Context) *State {
	state, _ := ctx.Value(stateKey).(*State)
	if state != nil {
		return state.WithContext(ctx)
	}
	return nil
}

func init() {
	ws.EnableRawEvents = true
}

// Wrap wraps the given state.
func Wrap(state *state.State) *State {
	state.Client.OnRequest = append(state.Client.OnRequest, func(r httpdriver.Request) error {
		req := (*http.Request)(r.(*httpdriver.DefaultRequest))
		log.Println("Discord API:", req.Method, req.URL.Path)
		return nil
	})

	dir := filepath.Join(os.TempDir(), "gtkcord4-events")
	os.RemoveAll(dir)

	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		log.Println("cannot mkdir -p for ev logginf:", err)
	}

	var atom uint64
	state.AddHandler(func(ev *ws.RawEvent) {
		id := atomic.AddUint64(&atom, 1)

		f, err := os.Create(filepath.Join(
			dir,
			fmt.Sprintf("%05d-%d-%s.json", id, ev.OriginalCode, ev.OriginalType),
		))
		if err != nil {
			log.Println("cannot log op:", err)
			return
		}
		defer f.Close()

		if _, err := f.Write(ev.Raw); err != nil {
			log.Println("event json error:", err)
		}
	})

	return &State{
		State: ningen.FromState(state),
	}
}

// InjectState injects the given state to a new context.
func InjectState(ctx context.Context, state *State) context.Context {
	return context.WithValue(ctx, stateKey, state)
}

// WithContext creates a copy of State with a new context.
func (s *State) WithContext(ctx context.Context) *State {
	return &State{
		State: s.State.WithContext(ctx),
	}
}

// BindHandler is similar to BindWidgetHandler, except the lifetime of the
// handler is bound to the context.
func (s *State) BindHandler(ctx gtkutil.Cancellable, fn func(gateway.Event), filters ...gateway.Event) {
	ctx.OnRenew(func(context.Context) func() {
		return s.AddSyncHandler(func(ev gateway.Event) {
			// Optionally filter out events.
			if len(filters) > 0 {
				for _, filter := range filters {
					if filter.Op() == ev.Op() && filter.EventType() == ev.EventType() {
						goto filtered
					}
				}
				return
			}

		filtered:
			glib.IdleAdd(func() { fn(ev) })
		})
	})
}

// AuthorMarkup renders the markup for the message author's name. It makes no
// API calls.
func (s *State) AuthorMarkup(m *gateway.MessageCreateEvent, mods ...author.MarkupMod) string {
	name := m.Author.Username

	if m.GuildID.IsValid() {
		member := m.Member
		if member == nil {
			log.Println(m.Author.Username, "missing from message")
			member, _ = s.Cabinet.Member(m.GuildID, m.Author.ID)
		}
		if member == nil {
			log.Println(m.Author.Username, "missing from state, requesting...")
			s.MemberState.RequestMember(m.GuildID, m.Author.ID)
		}

		if member != nil && member.Nick != "" {
			name = member.Nick
		}

		c, ok := state.MemberColor(member, func(id discord.RoleID) *discord.Role {
			role, _ := s.Cabinet.Role(m.GuildID, id)
			return role
		})
		if ok {
			mods = append(mods, author.WithColor(c.String()))
		}
	}

	if m.Author.Bot {
		bot := "bot"
		if m.WebhookID.IsValid() {
			bot = "webhook"
		}

		mods = append(mods, author.WithSuffixMarkup(
			`<span color="#6f78db" weight="normal">(`+bot+`)</span>`,
		))
	}

	return author.Markup(name, mods...)
}

// InjectAvatarSize calls InjectSize with size being 64px.
func InjectAvatarSize(urlstr string) string {
	return InjectSize(urlstr, 64)
}

// InjectSize injects the size query parameter into the URL. Size is
// automatically scaled up to 2x or more.
func InjectSize(urlstr string, size int) string {
	if urlstr == "" {
		return ""
	}

	u, err := url.Parse(urlstr)
	if err != nil {
		return urlstr
	}

	if scale := gtkutil.ScaleFactor(); scale > 2 {
		size *= scale
	} else {
		size *= 2
	}

	// Round size up to the nearest power of 2.
	size = int(math.Exp2(math.Ceil(math.Log2(float64(size)))))

	q := u.Query()
	q.Set("size", strconv.Itoa(size))
	u.RawQuery = q.Encode()

	return u.String()
}

// EmojiURL returns a sized emoji URL.
func EmojiURL(emojiID string, gif bool) string {
	return InjectSize(discordmd.EmojiURL(emojiID, gif), 64)
}
