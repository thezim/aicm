package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"go-aws/internal/display"
	"go-aws/internal/ec2"
	"go-aws/internal/model"
)

type themeStyles struct {
	list         lipgloss.Style
	selected     lipgloss.Style
	headCell     lipgloss.Style
	status       lipgloss.Style
	transient    lipgloss.Style
	transientErr lipgloss.Style
	keys         lipgloss.Style
	title        lipgloss.Style
	help         lipgloss.Style
}

func color(s string) lipgloss.Color {
	if s == "default" {
		return lipgloss.Color("")
	}
	return lipgloss.Color(s)
}

func (a *App) styles() themeStyles {
	t := a.theme
	list := lipgloss.NewStyle().Foreground(color(t.ListForeground)).Background(color(t.ListBackground))
	status := lipgloss.NewStyle().Foreground(color(t.StatusForeground)).Background(color(t.StatusBackground))
	transient := lipgloss.NewStyle().Foreground(color(t.TransientForeground)).Background(color(t.TransientBackground))
	return themeStyles{
		list:         list,
		selected:     lipgloss.NewStyle().Foreground(color(t.SelectedForeground)).Background(color(t.SelectedBackground)),
		headCell:     lipgloss.NewStyle().Foreground(color(t.HeaderForeground)).Background(color(t.HeaderBackground)).Bold(true),
		status:       status,
		transient:    transient,
		transientErr: lipgloss.NewStyle().Foreground(color(t.TransientErrorForeground)).Background(color(t.TransientBackground)).Bold(true),
		keys:         lipgloss.NewStyle().Foreground(color(t.KeysForeground)).Background(color(t.KeysBackground)),
		title:        lipgloss.NewStyle().Foreground(color(t.HeaderForeground)).Background(color(t.ListBackground)).Bold(true),
		help:         lipgloss.NewStyle().Foreground(color(t.HelpForeground)).Background(color(t.HelpBackground)),
	}
}

// View renders the current screen.
func (a *App) View() string {
	if a.width <= 0 || a.height <= 0 {
		return "go-aws: starting…"
	}
	st := a.styles()
	bar := a.renderStatusBar(st)
	switch a.view {
	case viewHelp:
		return a.joinScreen(st, bar, a.renderHelp(st))
	case viewError:
		return a.joinScreen(st, bar, a.renderError(st))
	case viewInstanceInfo:
		return a.joinScreen(st, bar, a.renderInstanceInfo(st))
	}
	if a.loading {
		return a.joinScreen(st, bar, a.renderLoading(st))
	}
	var body []string
	if a.view == viewInstances {
		body = a.renderInstances(st)
	} else {
		body = a.renderConnections(st)
	}
	status := a.renderStatus(st)
	footer := a.renderFooter(st)
	lines := make([]string, 0, a.height)
	lines = append(lines, body...)
	lines = append(lines, status, footer, bar)
	return strings.Join(lines, "\n")
}

func (a *App) joinScreen(st themeStyles, bar, body string) string {
	status := a.renderStatus(st)
	footer := a.renderFooter(st)
	fill := st.list
	if a.view == viewHelp {
		fill = st.help
	}
	bodyLines := strings.Split(body, "\n")
	want := a.height - 3
	if want < 1 {
		want = 1
	}
	if len(bodyLines) > want {
		bodyLines = bodyLines[:want]
	}
	for len(bodyLines) < want {
		bodyLines = append(bodyLines, fill.Render(strings.Repeat(" ", a.width)))
	}
	lines := bodyLines
	lines = append(lines, status, footer, bar)
	return strings.Join(lines, "\n")
}

func (a *App) renderStatusBar(st themeStyles) string {
	left := " go-aws"
	if a.accountID != "" {
		left += " · " + a.accountID
	}
	if a.region != "" {
		left += " · " + a.region
	}
	if a.profile != "" {
		left += " · " + a.profile
	}
	daemonState := "daemon down"
	if a.client != nil {
		daemonState = "daemon ok"
	}
	right := fmt.Sprintf("%d tunnel(s) · %s ", len(a.conns), daemonState)
	line := leftRight(left, right, a.width)
	return st.status.Render(line)
}

func (a *App) renderInstances(st themeStyles) []string {
	page := a.pageSize()
	cols := a.tableCols(a.contentWidth())
	headerCells := []string{"NAME"}
	widths := []int{cols.name}
	if cols.showState {
		headerCells = append(headerCells, "STATE")
		widths = append(widths, cols.state)
	}
	if cols.showID {
		headerCells = append(headerCells, "INSTANCE ID")
		widths = append(widths, cols.id)
	}
	if cols.showType {
		headerCells = append(headerCells, "TYPE")
		widths = append(widths, cols.typ)
	}
	if cols.showIP {
		headerCells = append(headerCells, "PRIVATE IP")
		widths = append(widths, cols.ip)
	}
	if cols.showPlatform {
		headerCells = append(headerCells, "PLATFORM")
		widths = append(widths, cols.platform)
	}
	headerCells = append(headerCells, "TUNNELS")
	widths = append(widths, cols.tunnels)
	headLine := a.joinCells(headerCells, widths)
	lines := make([]string, 0, page+1)
	lines = append(lines, a.chromeRow(st, headLine))
	if len(a.visibleNamed) == 0 {
		msg := "No instances found."
		if a.filterQueries[a.filterSlot(viewInstances)] != "" {
			msg = fmt.Sprintf("No instances match %q.", a.filterQueries[a.filterSlot(viewInstances)])
		} else if len(a.states) > 0 {
			msg = "No instances found in state(s): " + strings.Join(a.states, ", ")
		}
		lines = append(lines, st.list.Render(display.Pad(msg, a.width)))
		for i := 1; i < page; i++ {
			lines = append(lines, st.list.Render(strings.Repeat(" ", a.width)))
		}
		return lines
	}
	for row := 0; row < page; row++ {
		idx := a.offset + row
		var content string
		var style lipgloss.Style
		if idx < len(a.visibleNamed) {
			n := a.visibleNamed[idx]
			inst := findInstance(a.instances, n.InstanceID)
			cells := []string{display.TruncateDisplay(n, cols.name)}
			rowWidths := []int{cols.name}
			if cols.showState {
				cells = append(cells, inst.State)
				rowWidths = append(rowWidths, cols.state)
			}
			if cols.showID {
				cells = append(cells, inst.InstanceID)
				rowWidths = append(rowWidths, cols.id)
			}
			if cols.showType {
				cells = append(cells, inst.Type)
				rowWidths = append(rowWidths, cols.typ)
			}
			if cols.showIP {
				cells = append(cells, inst.PrivateIP)
				rowWidths = append(rowWidths, cols.ip)
			}
			if cols.showPlatform {
				platform := inst.Platform
				if platform == "" {
					platform = "-"
				}
				cells = append(cells, platform)
				rowWidths = append(rowWidths, cols.platform)
			}
			cells = append(cells, strconv.Itoa(a.tunnelCount(n.InstanceID)))
			rowWidths = append(rowWidths, cols.tunnels)
			content = a.joinCells(cells, rowWidths)
			style = st.list
			if idx == a.selected {
				style = st.selected
			}
		} else {
			content = strings.Repeat(" ", a.contentWidth())
			style = st.list
		}
		content += a.scrollbarChar(row, page, len(a.visibleNamed), a.offset)
		lines = append(lines, style.Render(display.Pad(content, a.width)))
	}
	return lines
}

func (a *App) renderConnections(st themeStyles) []string {
	page := a.pageSize()
	cols := a.connCols(a.contentWidth())
	headerCells := []string{"NAME"}
	widths := []int{cols.name}
	if cols.showAccount {
		headerCells = append(headerCells, "ACCOUNT")
		widths = append(widths, cols.account)
	}
	headerCells = append(headerCells, "PORT", "STATE")
	widths = append(widths, cols.port, cols.state)
	if cols.showAge {
		headerCells = append(headerCells, "AGE")
		widths = append(widths, cols.age)
	}
	if cols.showID {
		headerCells = append(headerCells, "ID")
		widths = append(widths, cols.id)
	}
	headLine := a.joinCells(headerCells, widths)
	lines := make([]string, 0, page+1)
	lines = append(lines, a.chromeRow(st, headLine))
	if len(a.visibleConns) == 0 {
		msg := "No active tunnels. Press tab to return to instances."
		if a.filterQueries[a.filterSlot(viewConnections)] != "" {
			msg = fmt.Sprintf("No connections match %q.", a.filterQueries[a.filterSlot(viewConnections)])
		}
		lines = append(lines, st.list.Render(display.Pad(msg, a.width)))
		for i := 1; i < page; i++ {
			lines = append(lines, st.list.Render(strings.Repeat(" ", a.width)))
		}
		return lines
	}
	for row := 0; row < page; row++ {
		idx := a.connOffset + row
		var content string
		style := st.list
		if idx < len(a.visibleConns) {
			c := a.conns[a.visibleConns[idx]]
			cells := []string{a.connDisplay(c)}
			rowWidths := []int{cols.name}
			if cols.showAccount {
				cells = append(cells, c.AccountID)
				rowWidths = append(rowWidths, cols.account)
			}
			cells = append(cells, strconv.Itoa(c.LocalPort), stateLabel(c))
			rowWidths = append(rowWidths, cols.port, cols.state)
			if cols.showAge {
				cells = append(cells, age(c.StartedAt))
				rowWidths = append(rowWidths, cols.age)
			}
			if cols.showID {
				cells = append(cells, c.ID)
				rowWidths = append(rowWidths, cols.id)
			}
			content = a.joinCells(cells, rowWidths)
			if idx == a.connSel {
				style = st.selected
			}
		} else {
			content = strings.Repeat(" ", a.contentWidth())
		}
		content += a.scrollbarChar(row, page, len(a.visibleConns), a.connOffset)
		lines = append(lines, style.Render(display.Pad(content, a.width)))
	}
	return lines
}

func (a *App) renderInstanceInfo(st themeStyles) string {
	page := a.infoPageSize()
	lines := a.instanceInfoLines(a.contentWidth())
	offset := a.infoOffset
	if max := len(lines) - page; offset > max {
		offset = max
	}
	if offset < 0 {
		offset = 0
	}
	var b strings.Builder
	for row := 0; row < page; row++ {
		idx := offset + row
		text := ""
		if idx >= 0 && idx < len(lines) {
			text = lines[idx]
		}
		text = display.Truncate(text, a.contentWidth())
		text += a.scrollbarChar(row, page, len(lines), offset)
		if row > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(st.list.Render(display.Pad(text, a.width)))
	}
	return b.String()
}

func (a *App) instanceInfoLines(width int) []string {
	if width < 1 {
		width = 1
	}
	if len(a.visibleNamed) == 0 || a.selected < 0 || a.selected >= len(a.visibleNamed) {
		return []string{"", "  No instance selected."}
	}
	named := a.visibleNamed[a.selected]
	inst := findInstance(a.instances, named.InstanceID)
	lines := []string{"", "  INSTANCE INFO", ""}
	lines = appendInfoField(lines, "Name", named.Display, width)
	lines = appendInfoField(lines, "Instance ID", inst.InstanceID, width)
	lines = appendInfoField(lines, "State", inst.State, width)
	lines = appendInfoField(lines, "Type", inst.Type, width)
	lines = appendInfoField(lines, "Private IP", inst.PrivateIP, width)
	lines = appendInfoField(lines, "Platform", inst.Platform, width)
	lines = appendInfoField(lines, "Availability zone", inst.AvailabilityZone, width)
	lines = appendInfoField(lines, "Launch time", formatLaunchTime(inst.LaunchTime), width)
	lines = appendInfoField(lines, "Tunnels", strconv.Itoa(a.tunnelCount(named.InstanceID)), width)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  TAGS (%d)", len(inst.Tags)))
	if len(inst.Tags) == 0 {
		lines = append(lines, "    No tags")
		return lines
	}
	tags := sortedTags(inst.Tags)
	keyWidth, valueWidth := tagColumnWidths(tags, width)
	lines = append(lines, "    "+display.Pad("KEY", keyWidth)+"  VALUE")
	for _, tag := range tags {
		lines = appendInfoTagRow(lines, tag, keyWidth, valueWidth)
	}
	return lines
}

func appendInfoField(lines []string, label, value string, width int) []string {
	if value == "" {
		value = "-"
	}
	prefix := fmt.Sprintf("  %-17s  ", label)
	available := width - runewidth.StringWidth(prefix)
	if available < 1 {
		available = 1
	}
	parts := wrapText(value, available)
	lines = append(lines, prefix+parts[0])
	indent := strings.Repeat(" ", runewidth.StringWidth(prefix))
	for _, part := range parts[1:] {
		lines = append(lines, indent+part)
	}
	return lines
}

func tagColumnWidths(tags []ec2.Tag, width int) (int, int) {
	const (
		indent = 4
		gap    = 2
		minKey = 3
	)
	inner := width - indent
	if inner < minKey+gap+1 {
		inner = minKey + gap + 1
	}
	maxKey := inner / 3
	if maxKey < minKey {
		maxKey = minKey
	}
	longest := minKey
	for _, tag := range tags {
		key := tag.Key
		if key == "" {
			key = "(no key)"
		}
		if w := runewidth.StringWidth(key); w > longest {
			longest = w
		}
	}
	keyWidth := longest
	if keyWidth > maxKey {
		keyWidth = maxKey
	}
	valueWidth := inner - keyWidth - gap
	if valueWidth < 1 {
		valueWidth = 1
	}
	return keyWidth, valueWidth
}

func appendInfoTagRow(lines []string, tag ec2.Tag, keyWidth, valueWidth int) []string {
	key := tag.Key
	if key == "" {
		key = "(no key)"
	}
	prefix := "    " + display.Pad(key, keyWidth) + "  "
	parts := wrapText(tag.Value, valueWidth)
	lines = append(lines, prefix+parts[0])
	indent := strings.Repeat(" ", 4+keyWidth+2)
	for _, part := range parts[1:] {
		lines = append(lines, indent+part)
	}
	return lines
}

func sortedTags(tags []ec2.Tag) []ec2.Tag {
	out := make([]ec2.Tag, 0, len(tags))
	for _, tag := range tags {
		out = append(out, ec2.Tag{
			Key:   display.Sanitize(tag.Key),
			Value: display.Sanitize(tag.Value),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := strings.ToLower(out[i].Key), strings.ToLower(out[j].Key)
		if ki != kj {
			return ki < kj
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func formatLaunchTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

func wrapText(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	var current []rune
	currentWidth := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		out = append(out, string(current))
		current = nil
		currentWidth = 0
	}
	for _, word := range words {
		wordWidth := runewidth.StringWidth(word)
		if currentWidth > 0 && currentWidth+1+wordWidth > width {
			flush()
		}
		if wordWidth > width {
			for _, r := range word {
				rw := runewidth.RuneWidth(r)
				if currentWidth > 0 && currentWidth+rw > width {
					flush()
				}
				current = append(current, r)
				currentWidth += rw
			}
			continue
		}
		if currentWidth > 0 {
			current = append(current, ' ')
			currentWidth++
		}
		current = append(current, []rune(word)...)
		currentWidth += wordWidth
	}
	flush()
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func (a *App) renderHelp(st themeStyles) string {
	help := []string{
		"  NAVIGATION",
		"    ↑/↓ or j/k        move selection",
		"    PgUp/PgDn        move one page",
		"    Home/End or g/G  first/last",
		"    tab              Instances / Connections",
		"    mouse            wheel scroll, click select",
		"  FILTER",
		"    /                filter the current table (all columns, case-insensitive)",
		"    enter keep · ctrl+u clear · esc cancel edit / clear filter",
		"  INSTANCES",
		"    i info · c console · r RDP (Windows) · s start · S stop · R refresh",
		"  INSTANCE INFO",
		"    ↑/↓ j/k scroll · PgUp/PgDn page · g/G jump · i/esc back",
		"  CONNECTIONS",
		"    o/enter reopen .rdp · x x close tunnel",
		"  CONFIG",
		"    e edit the config file, then reload it",
		"  GENERAL",
		"    ?/esc/q close help · ctrl+c quit (tunnels keep running)",
		"  Press esc, ?, q, or h to return.",
	}
	var b strings.Builder
	for i, line := range help {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(st.help.Render(display.Pad(line, a.width)))
	}
	return b.String()
}

func (a *App) renderError(st themeStyles) string {
	lines := []string{
		"",
		"  Startup failed",
		"",
	}
	width := a.width - 6
	if width < 20 {
		width = a.width - 2
	}
	for _, line := range wrap(a.fatal, width) {
		lines = append(lines, "  "+line)
	}
	if a.fatalHint != "" {
		lines = append(lines, "")
		for _, line := range wrap(a.fatalHint, width) {
			lines = append(lines, "  "+line)
		}
	}
	lines = append(lines, "", "  [R] retry    [q] quit")
	return a.styledBody(st, lines, 1)
}

func (a *App) renderLoading(st themeStyles) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame := frames[a.spinner%len(frames)]
	lines := []string{
		"",
		"  go-aws",
		"",
		"  " + frame + " Resolving AWS configuration and loading EC2 instances…",
		"",
		"  Press q to quit.",
	}
	return a.styledBody(st, lines, 1)
}

func (a *App) styledBody(st themeStyles, lines []string, titleLine int) string {
	var b strings.Builder
	for i := 0; i < a.height-3; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		text := ""
		if i < len(lines) {
			text = lines[i]
		}
		style := st.list
		if i == titleLine {
			style = st.title
		}
		b.WriteString(style.Render(display.Pad(text, a.width)))
	}
	return b.String()
}

func (a *App) renderStatus(st themeStyles) string {
	if a.filterEditing {
		matched, total := a.filterStats(a.filterView)
		prompt := fmt.Sprintf(" /%s▌   %d/%d matches", string(a.filterInput), matched, total)
		return st.transient.Render(display.Pad(prompt, a.width))
	}
	if a.status != "" {
		text := a.status
		if (a.pendingReady || a.consolePending || a.actionPending) && !a.statusErr {
			frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			text = frames[a.spinner%len(frames)] + " " + text
		}
		if a.statusErr {
			return st.transientErr.Render(display.Pad(" "+text, a.width))
		}
		return st.transient.Render(display.Pad(" "+text, a.width))
	}
	if query := a.currentFilter(); query != "" {
		matched, total := a.filterStats(a.view)
		text := fmt.Sprintf(" filter: %s · %d/%d · esc clear", query, matched, total)
		return st.transient.Render(display.Pad(text, a.width))
	}
	return st.transient.Render(display.Pad(" ", a.width))
}

func (a *App) renderFooter(st themeStyles) string {
	var text string
	switch a.view {
	case viewInstances:
		text = " ↑↓/jk · i info · c console · r RDP · s/S · R · tab · / filter · e edit · ? · q"
	case viewInstanceInfo:
		text = " ↑↓/jk scroll · PgUp/PgDn · g/G · i/esc back · e edit · ? help · q quit"
	case viewConnections:
		text = " ↑↓/jk · o open · x close · tab instances · / filter · e edit · ? · q quit"
	case viewError:
		text = " e edit config · R retry · q quit"
	default:
		text = " esc/?/q back · e edit config"
	}
	return st.keys.Render(display.Pad(text, a.width))
}

func (a *App) chromeRow(st themeStyles, content string) string {
	content += " "
	return st.headCell.Render(display.Pad(content, a.width))
}

func (a *App) contentWidth() int {
	if a.width > 1 {
		return a.width - 1
	}
	return a.width
}

type tableCols struct {
	name, state, id, typ, ip, platform, tunnels       int
	showState, showID, showType, showIP, showPlatform bool
}

func (a *App) tableCols(w int) tableCols {
	c := tableCols{showState: true, showID: true, showType: true, showIP: true, showPlatform: true}
	c.state, c.id, c.typ, c.ip, c.platform, c.tunnels = 10, 19, 12, 15, 11, 7
	seps := 6
	others := c.state + c.id + c.typ + c.ip + c.platform + c.tunnels + seps
	if w-others < 10 {
		c.showPlatform = false
		seps--
		others = c.state + c.id + c.typ + c.ip + c.tunnels + seps
	}
	if w-others < 10 {
		c.showType = false
		seps--
		others = c.state + c.id + c.ip + c.tunnels + seps
	}
	if w-others < 10 {
		c.showIP = false
		seps--
		others = c.state + c.id + c.tunnels + seps
	}
	c.name = w - others
	if c.name < 6 {
		c.name = 6
	}
	if c.state > 10 {
		c.state = 10
	}
	return c
}

type connTableCols struct {
	name, account, port, state, age, id int
	showAccount, showAge, showID        bool
}

func (a *App) connCols(w int) connTableCols {
	c := connTableCols{showAccount: true, showAge: true, showID: true}
	c.account, c.port, c.state, c.age, c.id = 12, 7, 10, 6, 10
	seps := 5
	others := c.account + c.port + c.state + c.age + c.id + seps
	if w-others < 12 {
		c.showAccount = false
		seps--
		others = c.port + c.state + c.age + c.id + seps
	}
	if w-others < 12 {
		c.showID = false
		seps--
		others = c.port + c.state + c.age + seps
	}
	if w-others < 12 {
		c.showAge = false
		seps--
		others = c.port + c.state + seps
	}
	c.name = w - others
	if c.name < 6 {
		c.name = 6
	}
	return c
}

func (a *App) joinCells(cells []string, widths []int) string {
	out := make([]string, 0, len(cells))
	for i, cell := range cells {
		if i >= len(widths) {
			break
		}
		out = append(out, display.Pad(cell, widths[i]))
	}
	return strings.TrimRight(strings.Join(out, " "), " ")
}

func (a *App) scrollbarChar(row, page, total, offset int) string {
	if a.width < 5 || total <= page {
		return " "
	}
	thumb := page * page / total
	if thumb < 1 {
		thumb = 1
	}
	pos := 0
	if total > page {
		pos = offset * (page - thumb) / (total - page)
	}
	if row >= pos && row < pos+thumb {
		return "█"
	}
	return "│"
}

func stateLabel(c model.Connection) string {
	s := string(c.State)
	if c.RDPError != "" && c.State == model.StateReady {
		s = "ready!"
	}
	return s
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func leftRight(left, right string, width int) string {
	if runewidth.StringWidth(left)+runewidth.StringWidth(right) >= width {
		return display.Truncate(left+" "+right, width)
	}
	gap := width - runewidth.StringWidth(left) - runewidth.StringWidth(right)
	return left + strings.Repeat(" ", gap) + right
}

func wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	line := ""
	for _, word := range words {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if runewidth.StringWidth(candidate) > width && line != "" {
			out = append(out, line)
			line = word
			continue
		}
		line = candidate
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
