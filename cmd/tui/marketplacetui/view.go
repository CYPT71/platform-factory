package marketplacetui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/CYPT71/platform-factory/cmd/tui/kit"
)

var (
	titleStyle   = kit.TitleStyle
	dimStyle     = kit.DimStyle
	errorStyle   = kit.ErrorStyle
	successStyle = kit.SuccessStyle
	helpStyle    = kit.HelpStyle
	cursorStyle  = kit.CursorStyle
	boxStyle     = kit.BoxStyle
)

func (m *model) View() string {
	if m.state == viewDetail {
		return m.viewDetail()
	}
	return m.viewList()
}

func (m *model) viewList() string {
	var b strings.Builder
	installed := fmt.Sprintf("%d installed", len(m.installed))
	fmt.Fprintln(&b, m.wrapLine(titleStyle.Render("Marketplace")+dimStyle.Render("  "+installed)))
	boxInnerWidth := max(1, m.width-boxStyle.GetHorizontalFrameSize())
	if m.width <= 0 {
		boxInnerWidth = 60
	}
	if m.width > 0 && m.width < 12 {
		fmt.Fprintln(&b, m.wrapLine("Search "+m.search.View()))
	} else {
		fmt.Fprintln(&b, boxStyle.Width(boxInnerWidth).Render("Search  "+m.search.View()))
	}
	if len(m.list.Items()) == 0 {
		message := "No plugins match your search.  ctrl+l clear"
		if len(m.index.Plugins) == 0 {
			message = "No plugins indexed yet.\nAdd a source, then run: platform-factory marketplace sync"
		}
		fmt.Fprintln(&b, m.wrapLine(dimStyle.Render(message)))
	} else {
		fmt.Fprintln(&b, m.list.View())
	}
	if status := m.statusLine(); status != "" {
		fmt.Fprintln(&b, m.wrapLine(status))
	} else {
		fmt.Fprintln(&b)
	}
	filters := fmt.Sprintf("%d results  ·  %s", len(m.list.Items()), m.sortBy)
	if m.verifiedOnly {
		filters += "  ·  verified"
	}
	fmt.Fprint(&b, m.wrapLine(helpStyle.Render(filters+"   ↑/↓ navigate · enter open · tab sort · ctrl+v verified · esc quit")))
	return b.String()
}

func (m *model) viewDetail() string {
	var b strings.Builder
	entry := m.detail
	fmt.Fprintln(&b, m.wrapLine(titleStyle.Render(entry.Name)))
	fmt.Fprintln(&b, m.wrapLine(dimStyle.Render(entry.Repository)))
	if entry.Description != "" {
		fmt.Fprintln(&b, m.wrapLine(entry.Description))
	}
	if entry.Author != "" {
		fmt.Fprintln(&b, m.wrapLine(dimStyle.Render("by "+entry.Author)))
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, m.wrapLine(titleStyle.Render("Versions")))
	for i, release := range entry.Releases {
		line := fmt.Sprintf("  %s", release.Version)
		if release.Verified {
			line += "  ✓verified"
		}
		if release.Version == entry.LatestVersion {
			line += "  (latest)"
		}
		if installed, ok := m.installed[entry.Name]; ok && installed.Version == release.Version {
			line += "  [installed]"
		}
		if i == m.detailVersion {
			fmt.Fprintln(&b, m.wrapLine(cursorStyle.Render("▸"+line)))
		} else {
			fmt.Fprintln(&b, m.wrapLine(" "+line))
		}
	}
	fmt.Fprintln(&b)

	if release, ok := entry.Release(m.selectedVersion()); ok {
		fmt.Fprintln(&b, m.wrapLine(titleStyle.Render("Selected release: ")+release.Version))
		trust := errorStyle.Render("unverified")
		if release.Verified {
			trust = successStyle.Render("verified at sync")
		}
		fmt.Fprintln(&b, m.wrapLine("  trust:         "+trust))
		if len(release.Compatibility) > 0 {
			fmt.Fprintln(&b, m.wrapLine("  compatibility: "+strings.Join(release.Compatibility, ", ")))
		}
		fmt.Fprintln(&b, "  permissions:")
		fmt.Fprintln(&b, m.wrapLine("    network:    "+permissionList(release.Permissions.Network)))
		fmt.Fprintln(&b, m.wrapLine("    filesystem: "+permissionList(release.Permissions.Filesystem)))
		fmt.Fprintln(&b, m.wrapLine("    secrets:    "+permissionList(release.Permissions.Secrets)))
	}
	fmt.Fprintln(&b)

	if status := m.statusLine(); status != "" {
		fmt.Fprintln(&b, m.wrapLine(status))
	} else {
		fmt.Fprintln(&b)
	}
	if m.confirmRemove {
		fmt.Fprintln(&b, m.wrapLine(errorStyle.Render("Remove "+entry.Name+"? y confirm · any key cancel")))
		return b.String()
	}
	_, isInstalled := m.installed[entry.Name]
	actions := "i install selected version"
	if isInstalled {
		actions = "x remove"
		if m.installed[entry.Name].Version != m.selectedVersion() {
			actions = "u change to selected version  ·  " + actions
		}
	}
	if reason := m.installBlocked(); reason != "" && (!isInstalled || m.installed[entry.Name].Version != m.selectedVersion()) {
		actions = reason
	}
	fmt.Fprintln(&b, m.wrapLine(helpStyle.Render(actions+"   ↑/↓ choose version · esc back")))
	return b.String()
}

func (m *model) wrapLine(s string) string {
	if m.width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(m.width).Render(s)
}

func permissionList(values []string) string {
	if len(values) == 0 {
		return "(none requested)"
	}
	return strings.Join(values, ", ")
}

func (m *model) statusLine() string {
	if m.working {
		return titleStyle.Render("Working…")
	}
	if m.status == "" {
		return ""
	}
	if m.statusIsError {
		return errorStyle.Render("✗ " + m.status)
	}
	return successStyle.Render("✓ " + m.status)
}
