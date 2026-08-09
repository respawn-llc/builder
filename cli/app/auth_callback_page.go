package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"core/cli/app/internal/authui"
	sharedtheme "core/shared/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const authCallbackErrorDuration = 3 * time.Second

type authCallbackPageData struct {
	Theme        string
	AuthorizeURL string
	OpenErr      error
}

type authCallbackPageResult struct {
	Method        authui.AuthMethod
	CallbackInput string
	Canceled      bool
	Err           error
}

type authCallbackPageModel struct {
	data        authCallbackPageData
	width       int
	height      int
	input       string
	inputCursor int
	errorText   string
	errorToken  uint64
	ctx         context.Context
	complete    func(context.Context, string) (authui.AuthMethod, error)
	result      authCallbackPageResult
	styles      authCallbackPageStyles
}

type authCallbackPageStyles struct {
	title  lipgloss.Style
	body   lipgloss.Style
	meta   lipgloss.Style
	url    lipgloss.Style
	error  lipgloss.Style
	border lipgloss.Style
	input  lipgloss.Style
}

type authCallbackPageErrorClearMsg struct {
	token uint64
}

type authCallbackPageBrowserDoneMsg struct {
	callback authui.OAuthBrowserCallback
	err      error
}

type authCallbackPageCompleteDoneMsg struct {
	input  string
	method authui.AuthMethod
	err    error
}

func newAuthCallbackPageModel(data authCallbackPageData) *authCallbackPageModel {
	return &authCallbackPageModel{
		data:        data,
		width:       defaultPickerWidth,
		height:      defaultPickerHeight,
		inputCursor: -1,
		styles:      newAuthCallbackPageStyles(data.Theme),
	}
}

func newAuthCallbackPageStyles(theme string) authCallbackPageStyles {
	palette := uiPalette(theme)
	return authCallbackPageStyles{
		title:  lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		body:   lipgloss.NewStyle().Foreground(palette.foreground),
		meta:   lipgloss.NewStyle().Foreground(palette.muted).Faint(true),
		url:    lipgloss.NewStyle().Foreground(palette.primary).Underline(true),
		error:  lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true),
		border: lipgloss.NewStyle().Foreground(palette.primary),
		input:  lipgloss.NewStyle().Foreground(palette.foreground),
	}
}

func (m *authCallbackPageModel) Init() tea.Cmd { return nil }

func (m *authCallbackPageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.result = authCallbackPageResult{Canceled: true}
			return m, tea.Quit
		case tea.KeyEnter:
			input := strings.TrimSpace(m.input)
			if input == "" {
				return m, m.showError("Paste the callback URL or code first.")
			}
			return m, m.completeInput(input)
		case tea.KeyBackspace, tea.KeyCtrlH:
			m.backspaceInput()
		case tea.KeyDelete:
			m.deleteInput()
		case tea.KeyLeft:
			m.moveInputCursor(-1)
		case tea.KeyRight:
			m.moveInputCursor(1)
		case tea.KeyHome, tea.KeyCtrlA:
			m.inputCursor = 0
		case tea.KeyEnd, tea.KeyCtrlE:
			m.inputCursor = len([]rune(m.input))
		case tea.KeyRunes:
			m.insertInputRunes(msg.Runes)
		}
	case authCallbackPageErrorClearMsg:
		if msg.token == m.errorToken {
			m.errorText = ""
		}
	case authCallbackPageBrowserDoneMsg:
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.result = authCallbackPageResult{Err: msg.err}
				return m, tea.Quit
			}
			return m, m.showError("Browser callback failed: " + msg.err.Error() + ". Paste the callback URL or code.")
		}
		return m, m.completeInput(browserCallbackInput(msg.callback))
	case authCallbackPageCompleteDoneMsg:
		if msg.err != nil {
			return m, m.showError("Invalid callback: " + msg.err.Error())
		}
		m.result = authCallbackPageResult{Method: msg.method, CallbackInput: msg.input}
		return m, tea.Quit
	}
	return m, nil
}

func (m *authCallbackPageModel) View() string {
	width := max(1, m.width)
	contentWidth := width - 2
	if contentWidth < 1 {
		contentWidth = 1
	}
	var lines []string
	lines = append(lines, strings.Split(renderStartupBanner(startupBannerANSI), "\n")...)
	lines = append(lines, "", m.styles.title.Render("Sign in with OpenAI Codex"))
	lines = append(lines, m.styles.body.Render("Complete sign-in in your browser, or paste the callback URL/code below."))
	if strings.TrimSpace(m.data.AuthorizeURL) != "" {
		lines = append(lines, m.styles.url.Render(truncateQueuedMessageLine(m.data.AuthorizeURL, contentWidth)))
	}
	if m.data.OpenErr != nil {
		lines = append(lines, m.styles.meta.Render(truncateQueuedMessageLine("Browser did not open automatically: "+m.data.OpenErr.Error(), contentWidth)))
	} else {
		lines = append(lines, m.styles.meta.Render("Waiting for browser callback..."))
	}
	lines = append(lines, "")
	inputLines := renderFramedEditableInputLines(contentWidth, 1, uiEditableInputRenderSpec{
		Prefix:       "› ",
		Text:         m.input,
		CursorIndex:  m.inputCursor,
		RenderCursor: true,
		Placeholder:  "Paste callback URL or code",
	}, m.styles.input, m.styles.border)
	lines = append(lines, inputLines...)
	if strings.TrimSpace(m.errorText) != "" {
		lines = append(lines, m.styles.error.Render(truncateQueuedMessageLine(m.errorText, contentWidth)))
	}
	lines = append(lines, m.styles.meta.Render("Enter submits pasted code. Esc cancels."))
	body := strings.Join(lines, "\n")
	return lipgloss.Place(width, max(1, m.height), lipgloss.Left, lipgloss.Top, body)
}

func (m *authCallbackPageModel) showError(text string) tea.Cmd {
	m.errorToken++
	m.errorText = strings.TrimSpace(text)
	token := m.errorToken
	return tea.Tick(authCallbackErrorDuration, func(time.Time) tea.Msg {
		return authCallbackPageErrorClearMsg{token: token}
	})
}

func (m *authCallbackPageModel) completeInput(input string) tea.Cmd {
	complete := m.complete
	if complete == nil {
		return func() tea.Msg {
			return authCallbackPageCompleteDoneMsg{err: errors.New("auth callback completion is required")}
		}
	}
	return func() tea.Msg {
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		method, err := complete(ctx, input)
		return authCallbackPageCompleteDoneMsg{input: input, method: method, err: err}
	}
}

func (m *authCallbackPageModel) insertInputRunes(runes []rune) {
	filtered, _ := stripMouseSGRRunes(runes)
	if len(filtered) == 0 {
		return
	}
	current := []rune(m.input)
	cursor := m.normalizedInputCursor(current)
	next := make([]rune, 0, len(current)+len(filtered))
	next = append(next, current[:cursor]...)
	next = append(next, filtered...)
	next = append(next, current[cursor:]...)
	m.input = string(next)
	m.inputCursor = cursor + len(filtered)
}

func (m *authCallbackPageModel) backspaceInput() {
	current := []rune(m.input)
	cursor := m.normalizedInputCursor(current)
	if cursor == 0 {
		return
	}
	next := make([]rune, 0, len(current)-1)
	next = append(next, current[:cursor-1]...)
	next = append(next, current[cursor:]...)
	m.input = string(next)
	m.inputCursor = cursor - 1
}

func (m *authCallbackPageModel) deleteInput() {
	current := []rune(m.input)
	cursor := m.normalizedInputCursor(current)
	if cursor >= len(current) {
		return
	}
	next := make([]rune, 0, len(current)-1)
	next = append(next, current[:cursor]...)
	next = append(next, current[cursor+1:]...)
	m.input = string(next)
	m.inputCursor = cursor
}

func (m *authCallbackPageModel) moveInputCursor(delta int) {
	current := []rune(m.input)
	cursor := m.normalizedInputCursor(current) + delta
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(current) {
		cursor = len(current)
	}
	m.inputCursor = cursor
}

func (m *authCallbackPageModel) normalizedInputCursor(current []rune) int {
	if m.inputCursor < 0 || m.inputCursor > len(current) {
		return len(current)
	}
	return m.inputCursor
}

var runAuthCallbackPage = func(ctx context.Context, data authCallbackPageData, waitCallback func(context.Context) (authui.OAuthBrowserCallback, error), complete func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error) {
	model := newAuthCallbackPageModel(data)
	model.ctx = ctx
	model.complete = complete
	program := tea.NewProgram(model, tea.WithAltScreen())
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	if waitCallback != nil {
		go func() {
			callback, err := waitCallback(waitCtx)
			program.Send(authCallbackPageBrowserDoneMsg{callback: callback, err: err})
		}()
	}
	finalModel, err := program.Run()
	if err != nil {
		return authCallbackPageResult{}, err
	}
	page, ok := finalModel.(*authCallbackPageModel)
	if !ok {
		return authCallbackPageResult{}, nil
	}
	return page.result, nil
}

func browserCallbackInput(callback authui.OAuthBrowserCallback) string {
	query := url.Values{
		"code":  []string{callback.Code},
		"state": []string{callback.State},
	}
	return query.Encode()
}
