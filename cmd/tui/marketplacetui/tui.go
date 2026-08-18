package marketplacetui

import (
	"context"
	"crypto/ed25519"
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/CYPT71/platform-factory/cmd/tui/kit"
	"github.com/CYPT71/platform-factory/internal/marketplace"
)

// Config locates marketplace state used by Run.
type Config struct {
	IndexPath     string
	PluginsDir    string
	HostVersion   string
	TrustedKeys   []ed25519.PublicKey
	AllowUnsigned bool
}

type viewState uint8

const (
	viewList viewState = iota
	viewDetail
)

type actionResult struct {
	status string
	err    error
}

type model struct {
	index         *marketplace.Index
	manager       *marketplace.Manager
	installed     map[string]marketplace.InstalledPlugin
	search        textinput.Model
	list          list.Model
	state         viewState
	detail        marketplace.PluginEntry
	detailVersion int
	sortBy        marketplace.SortBy
	verifiedOnly  bool
	working       bool
	confirmRemove bool
	status        string
	statusIsError bool
	width         int
	height        int
}

// Run opens the marketplace browser on the process terminal.
func Run(config Config) error {
	index, err := marketplace.LoadIndex(config.IndexPath)
	if err != nil {
		return err
	}
	manager := &marketplace.Manager{
		Dir: config.PluginsDir, HostVersion: config.HostVersion,
		TrustedKeys: config.TrustedKeys, AllowUnsigned: config.AllowUnsigned,
	}
	installed, err := manager.Installed()
	if err != nil {
		return err
	}
	search := textinput.New()
	search.Placeholder = "name, author, description, tag"
	search.Focus()
	m := &model{
		index: index, manager: manager, search: search,
		list:      list.New(nil, list.NewDefaultDelegate(), 80, 20),
		installed: make(map[string]marketplace.InstalledPlugin), sortBy: marketplace.SortRelevance,
		width: 100, height: 30,
	}
	m.list.SetShowTitle(false)
	m.list.SetFilteringEnabled(false)
	m.list.SetShowHelp(false)
	m.list.SetShowStatusBar(false)
	for _, plugin := range installed {
		m.installed[plugin.Name] = plugin
	}
	m.refresh()
	return kit.LaunchVoid(m, tea.WithAltScreen())
}

func (m *model) Init() tea.Cmd { return textinput.Blink }

func (m *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	if kit.IsQuitKey(message) {
		return m, tea.Quit
	}
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.list.SetSize(max(message.Width-2, 20), max(message.Height-9, 5))
	case actionResult:
		m.working = false
		m.status, m.statusIsError = message.status, message.err != nil
		if message.err != nil {
			m.status = message.err.Error()
		} else {
			m.reloadInstalled()
		}
	case tea.KeyMsg:
		if m.working {
			return m, nil
		}
		m.status, m.statusIsError = "", false
		if m.state == viewDetail {
			return m.updateDetail(message)
		}
		switch message.String() {
		case "esc":
			return m, tea.Quit
		case "enter":
			if item, ok := m.list.SelectedItem().(pluginItem); ok {
				m.detail, m.detailVersion, m.state = item.entry, 0, viewDetail
			}
			return m, nil
		case "tab":
			order := []marketplace.SortBy{marketplace.SortRelevance, marketplace.SortPopularity, marketplace.SortVerified, marketplace.SortName, marketplace.SortDate}
			for index, value := range order {
				if value == m.sortBy {
					m.sortBy = order[(index+1)%len(order)]
					break
				}
			}
			m.refresh()
			return m, nil
		case "ctrl+v":
			m.verifiedOnly = !m.verifiedOnly
			m.refresh()
			return m, nil
		case "ctrl+l":
			m.search.SetValue("")
			m.refresh()
			return m, nil
		}
		previous := m.search.Value()
		var command tea.Cmd
		m.search, command = m.search.Update(message)
		commands = append(commands, command)
		if m.search.Value() != previous {
			m.refresh()
		}
	}
	var command tea.Cmd
	m.list, command = m.list.Update(message)
	commands = append(commands, command)
	return m, tea.Batch(commands...)
}

func (m *model) updateDetail(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmRemove {
		m.confirmRemove = false
		if key.String() == "y" {
			return m.startAction("remove")
		}
		m.status = "removal canceled"
		return m, nil
	}
	switch key.String() {
	case "esc":
		m.state = viewList
	case "up", "k":
		if m.detailVersion > 0 {
			m.detailVersion--
		}
	case "down", "j":
		if m.detailVersion+1 < len(m.detail.Releases) {
			m.detailVersion++
		}
	case "i":
		if _, installed := m.installed[m.detail.Name]; !installed {
			if reason := m.installBlocked(); reason != "" {
				m.status, m.statusIsError = reason, true
				return m, nil
			}
			return m.startAction("install")
		}
	case "u":
		if installed, ok := m.installed[m.detail.Name]; ok && installed.Version != m.selectedVersion() {
			if reason := m.installBlocked(); reason != "" {
				m.status, m.statusIsError = reason, true
				return m, nil
			}
			return m.startAction("update")
		}
	case "x":
		if _, installed := m.installed[m.detail.Name]; installed {
			m.confirmRemove = true
		}
	}
	return m, nil
}

func (m *model) startAction(action string) (tea.Model, tea.Cmd) {
	m.working, m.status, m.statusIsError = true, "", false
	name, version, manager, index := m.detail.Name, m.selectedVersion(), m.manager, m.index
	return m, func() tea.Msg {
		var err error
		switch action {
		case "install":
			_, err = manager.Install(context.Background(), index, name, version)
		case "update":
			_, err = manager.Update(context.Background(), index, name, version)
		case "remove":
			err = manager.Remove(name)
		}
		return actionResult{status: fmt.Sprintf("%s complete", action), err: err}
	}
}

func (m *model) selectedVersion() string {
	if len(m.detail.Releases) == 0 {
		return ""
	}
	return m.detail.Releases[m.detailVersion].Version
}

func (m *model) installBlocked() string {
	release, ok := m.detail.Release(m.selectedVersion())
	if !ok {
		return "no release selected"
	}
	if release.Verified && len(m.manager.TrustedKeys) == 0 {
		return "trusted key required; reopen with --key PUBLIC.pem"
	}
	if !release.Verified && !m.manager.AllowUnsigned {
		return "unverified release; review it and reopen with --allow-unsigned"
	}
	return ""
}

func (m *model) refresh() {
	result := marketplace.Search(m.index, marketplace.Request{
		Query: m.search.Value(), Filter: marketplace.Filter{VerifiedOnly: m.verifiedOnly},
		Sort: m.sortBy, Page: 1, PageSize: max(len(m.index.Plugins), 1),
	})
	items := make([]list.Item, 0, len(result.Hits))
	for _, hit := range result.Hits {
		version := ""
		if installed, ok := m.installed[hit.Plugin.Name]; ok {
			version = installed.Version
		}
		items = append(items, pluginItem{entry: hit.Plugin, installedVersion: version})
	}
	m.list.SetItems(items)
	m.list.Select(0)
}

func (m *model) reloadInstalled() {
	plugins, err := m.manager.Installed()
	if err != nil {
		m.status, m.statusIsError = err.Error(), true
		return
	}
	m.installed = make(map[string]marketplace.InstalledPlugin, len(plugins))
	for _, plugin := range plugins {
		m.installed[plugin.Name] = plugin
	}
	m.refresh()
}
