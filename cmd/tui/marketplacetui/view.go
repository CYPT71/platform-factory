package marketplacetui

import (
	"fmt"
	"strings"

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
	fmt.Fprintln(&b, titleStyle.Render("Marketplace")+dimStyle.Render("  "+installed))
	fmt.Fprintln(&b, boxStyle.Width(max(m.width-6, 16)).Render("Search  "+m.search.View()))
	if len(m.list.Items()) == 0 {
		message := "No plugins match your search.  ctrl+l clear"
		if len(m.index.Plugins) == 0 {
			message = "No plugins indexed yet.\nAdd a source, then run: platform-factory marketplace sync"
		}
		fmt.Fprintln(&b, dimStyle.Render(message))
	} else {
		fmt.Fprintln(&b, m.list.View())
	}
	fmt.Fprintln(&b, m.statusLine())
	filters := fmt.Sprintf("%d results  ·  %s", len(m.list.Items()), m.sortBy)
	if m.verifiedOnly {
		filters += "  ·  verified"
	}
	fmt.Fprint(&b, helpStyle.Render(filters+"   ↑/↓ navigate · enter open · tab sort · ctrl+v verified · esc quit"))
	return b.String()
}

func (m *model) viewDetail() string {
	var b strings.Builder
	entry := m.detail
	fmt.Fprintln(&b, titleStyle.Render(entry.Name))
	fmt.Fprintln(&b, dimStyle.MaxWidth(max(m.width-2, 20)).Render(entry.Repository))
	if entry.Description != "" {
		fmt.Fprintln(&b, entry.Description)
	}
	if entry.Author != "" {
		fmt.Fprintln(&b, dimStyle.Render("by "+entry.Author))
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, titleStyle.Render("Versions"))
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
			fmt.Fprintln(&b, cursorStyle.Render("▸"+line))
		} else {
			fmt.Fprintln(&b, " "+line)
		}
	}
	fmt.Fprintln(&b)

	if release, ok := entry.Release(m.selectedVersion()); ok {
		fmt.Fprintln(&b, titleStyle.Render("Selected release: ")+release.Version)
		trust := errorStyle.Render("unverified")
		if release.Verified {
			trust = successStyle.Render("verified at sync")
		}
		fmt.Fprintln(&b, "  trust:         "+trust)
		if len(release.Compatibility) > 0 {
			fmt.Fprintln(&b, "  compatibility: "+strings.Join(release.Compatibility, ", "))
		}
		fmt.Fprintln(&b, "  permissions:")
		fmt.Fprintln(&b, "    network:    "+permissionList(release.Permissions.Network))
		fmt.Fprintln(&b, "    filesystem: "+permissionList(release.Permissions.Filesystem))
		fmt.Fprintln(&b, "    secrets:    "+permissionList(release.Permissions.Secrets))
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, m.statusLine())
	if m.confirmRemove {
		fmt.Fprintln(&b, errorStyle.Render("Remove "+entry.Name+"? y confirm · any key cancel"))
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
	fmt.Fprintln(&b, helpStyle.Render(actions+"   ↑/↓ choose version · esc back"))
	return b.String()
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
