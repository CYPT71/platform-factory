// Command platform-factory-installer is an interactive terminal installer that
// builds and installs only the platform-factory binaries an end user selects,
// instead of the full command set scripts/local/bootstrap.sh produces for
// CI and cross-compilation. Run it from within the platform-factory-base repo:
//
//	go run ./cmd/platform-factory-installer
//
// or non-interactively:
//
//	go run ./cmd/platform-factory-installer -components builder,microvm -prefix ~/.local/bin -yes
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	subtleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	pendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	boxStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("212")).Padding(1, 3)
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render("error: ")+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		flagComponents = flag.String("components", "", "comma-separated component keys to install non-interactively (core is always included); see -list")
		flagPrefix     = flag.String("prefix", defaultPrefix(), "installation directory for binaries")
		flagYes        = flag.Bool("yes", false, "confirm and skip the interactive wizard (required together with -components outside a terminal)")
		flagList       = flag.Bool("list", false, "list available components and exit")
		flagGOOS       = flag.String("os", runtime.GOOS, "target OS")
		flagGOARCH     = flag.String("arch", runtime.GOARCH, "target architecture")
	)
	flag.Parse()

	if *flagList {
		printComponentTable()
		return nil
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}

	var (
		selectedKeys []string
		prefix       = *flagPrefix
	)

	// bubbletea and huh both need a real controlling terminal; CI runners,
	// piped output and redirected stdin have none, so both the wizard and
	// the animated build view are gated on isatty, with a plain sequential
	// fallback below for everything else.
	terminal := isatty.IsTerminal(os.Stdout.Fd())
	interactive := terminal && *flagComponents == "" && !*flagYes
	if interactive {
		selectedKeys, prefix, err = runWizard(prefix)
		if err != nil {
			return err
		}
	} else {
		if *flagComponents == "" && !*flagYes {
			return errors.New("not a terminal: pass -components and -yes to install non-interactively, or -list to see options")
		}
		if !*flagYes {
			return errors.New("non-interactive mode requires -yes to confirm")
		}
		selectedKeys = parseComponents(*flagComponents)
	}

	selected, err := resolveComponents(selectedKeys)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return fmt.Errorf("create install directory: %w", err)
	}

	steps := buildSteps(selected, *flagGOOS, *flagGOARCH)
	version := gitVersion(repoRoot)
	if terminal {
		model := newBuildModel(steps, repoRoot, prefix, *flagGOOS, *flagGOARCH, version)
		finalModel, err := tea.NewProgram(model).Run()
		if err != nil {
			return err
		}
		built := finalModel.(buildModel)
		if built.failed {
			for _, step := range built.steps {
				if step.status == statusFailed {
					return step.err
				}
			}
			return errors.New("installation failed")
		}
	} else if err := runPlainBuild(steps, repoRoot, prefix, *flagGOOS, *flagGOARCH, version); err != nil {
		return err
	}

	if err := ensurePFAlias(prefix, *flagGOOS); err != nil {
		return err
	}

	printSuccess(prefix, selected)
	return nil
}

// ensurePFAlias creates pf as an alias for platform-factory in prefix,
// but only if platform-factory was actually installed by this run. A
// symlink on POSIX (cheap, always in sync with a later platform-factory
// rebuild in place); a real file copy on Windows, where creating a
// symlink needs a privilege an ordinary installer run cannot assume.
func ensurePFAlias(prefix, goos string) error {
	suffix := binSuffix(goos)
	targetName := "platform-factory" + suffix
	target := filepath.Join(prefix, targetName)
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", targetName, err)
	}
	alias := filepath.Join(prefix, "pf"+suffix)
	_ = os.Remove(alias) // replace a stale alias from a previous install
	if goos == "windows" {
		data, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("read %s for pf alias: %w", targetName, err)
		}
		return os.WriteFile(alias, data, 0o755)
	}
	// Relative symlink target (just the filename, not prefix-qualified):
	// keeps the alias valid if the whole install directory is later moved.
	return os.Symlink(targetName, alias)
}

func defaultPrefix() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "bin")
	}
	return "./bin"
}

func findRepoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("locate go.mod: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || filepath.Base(gomod) != "go.mod" {
		return "", errors.New("run the installer from within the platform-factory-base repository (go.mod not found)")
	}
	return filepath.Dir(gomod), nil
}

func gitVersion(repoRoot string) string {
	out, err := exec.Command("git", "-C", repoRoot, "describe", "--tags", "--always", "--dirty").Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimSpace(string(out))
}

func printComponentTable() {
	fmt.Println(titleStyle.Render("Available components"))
	for _, c := range components {
		mark := "  "
		if c.mandatory {
			mark = "* "
		}
		fmt.Printf("%s%-12s %s\n", mark, c.key, c.description)
		fmt.Printf("    binaries: %s\n", strings.Join(c.binaries, ", "))
	}
	fmt.Println(subtleStyle.Render("\n* always installed"))
}

func runWizard(defaultInstallPrefix string) ([]string, string, error) {
	fmt.Println(boxStyle.Render(
		titleStyle.Render("platform-factory installer") + "\n" +
			subtleStyle.Render("Installs only the binaries you actually need."),
	))

	var (
		chosen    []string
		prefix    = defaultInstallPrefix
		confirmed bool
	)
	optOptions := make([]huh.Option[string], 0, len(components)-1)
	for _, c := range components {
		if c.mandatory {
			continue
		}
		optOptions = append(optOptions, huh.NewOption(fmt.Sprintf("%s — %s", c.label, c.description), c.key))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Additional components").
				Description("platform-factory (core CLI) is always installed.").
				Options(optOptions...).
				Value(&chosen),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Install directory").
				Value(&prefix).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("the install directory cannot be empty")
					}
					return nil
				}),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Build and install now?").
				Value(&confirmed),
		),
	).WithTheme(huh.ThemeCharm())

	if err := form.Run(); err != nil {
		return nil, "", err
	}
	if !confirmed {
		return nil, "", errors.New("installation cancelled")
	}
	return chosen, prefix, nil
}

type buildStatus int

const (
	statusPending buildStatus = iota
	statusBuilding
	statusDone
	statusFailed
)

type stepDoneMsg struct {
	index int
	err   error
}

type buildModel struct {
	steps    []buildStep
	current  int
	spinner  spinner.Model
	repoRoot string
	prefix   string
	goos     string
	goarch   string
	version  string
	done     bool
	failed   bool
}

func newBuildModel(steps []buildStep, repoRoot, prefix, goos, goarch, version string) buildModel {
	s := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("212"))))
	if len(steps) > 0 {
		steps[0].status = statusBuilding
	}
	return buildModel{
		steps:    steps,
		spinner:  s,
		repoRoot: repoRoot,
		prefix:   prefix,
		goos:     goos,
		goarch:   goarch,
		version:  version,
	}
}

func (m buildModel) Init() tea.Cmd {
	if len(m.steps) == 0 {
		return tea.Quit
	}
	return tea.Batch(m.spinner.Tick, m.runStep(m.current))
}

// buildBinary invokes `go build` for a single component binary. It is the
// one place that actually shells out, shared by the animated bubbletea
// view and the plain sequential fallback used outside a terminal.
func buildBinary(repoRoot, prefix, goos, goarch, version string, step buildStep) ([]byte, error) {
	out := filepath.Join(prefix, step.name+binSuffix(goos))
	ldflags := "-s -w"
	if step.name == "platform-factory" {
		ldflags += " -X main.version=" + version
	}
	cgo := "0"
	if step.cgo {
		cgo = "1"
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags="+ldflags, "-o", out, step.pkg)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED="+cgo, "GOOS="+goos, "GOARCH="+goarch)
	return cmd.CombinedOutput()
}

func runPlainBuild(steps []buildStep, repoRoot, prefix, goos, goarch, version string) error {
	for _, step := range steps {
		fmt.Printf("building %s... ", step.name)
		output, err := buildBinary(repoRoot, prefix, goos, goarch, version, step)
		if err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("%s: %w: %s", step.name, err, strings.TrimSpace(string(output)))
		}
		fmt.Println("ok")
	}
	return nil
}

func (m buildModel) runStep(i int) tea.Cmd {
	step := m.steps[i]
	repoRoot, prefix, goos, goarch, version := m.repoRoot, m.prefix, m.goos, m.goarch, m.version
	return func() tea.Msg {
		output, err := buildBinary(repoRoot, prefix, goos, goarch, version, step)
		if err != nil {
			return stepDoneMsg{index: i, err: fmt.Errorf("%s: %w: %s", step.name, err, strings.TrimSpace(string(output)))}
		}
		return stepDoneMsg{index: i}
	}
}

func (m buildModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case stepDoneMsg:
		if msg.err != nil {
			m.steps[msg.index].status = statusFailed
			m.steps[msg.index].err = msg.err
			m.failed = true
			m.done = true
			return m, tea.Quit
		}
		m.steps[msg.index].status = statusDone
		m.current++
		if m.current >= len(m.steps) {
			m.done = true
			return m, tea.Quit
		}
		m.steps[m.current].status = statusBuilding
		return m, m.runStep(m.current)
	}
	return m, nil
}

func (m buildModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Building selected binaries"))
	b.WriteString("\n\n")
	for _, step := range m.steps {
		switch step.status {
		case statusDone:
			b.WriteString(doneStyle.Render("✓ "))
			b.WriteString(step.name)
			b.WriteString("\n")
		case statusFailed:
			b.WriteString(failStyle.Render("✗ "))
			b.WriteString(step.name)
			b.WriteString("\n")
		case statusBuilding:
			b.WriteString(m.spinner.View())
			b.WriteString(" ")
			b.WriteString(step.name)
			b.WriteString("\n")
		default:
			b.WriteString(pendingStyle.Render("· " + step.name + "\n"))
		}
	}
	return b.String()
}

func printSuccess(prefix string, selected []component) {
	var names []string
	for _, c := range selected {
		names = append(names, c.binaries...)
	}
	sort.Strings(names)
	for i, name := range names {
		if name == "platform-factory" {
			names = append(names[:i+1], append([]string{"pf (alias)"}, names[i+1:]...)...)
			break
		}
	}

	var body strings.Builder
	body.WriteString(doneStyle.Render("Installation complete"))
	body.WriteString("\n\n")
	body.WriteString(fmt.Sprintf("Installed into %s:\n", prefix))
	for _, name := range names {
		body.WriteString("  • ")
		body.WriteString(name)
		body.WriteString("\n")
	}
	if !strings.Contains(os.Getenv("PATH"), prefix) {
		body.WriteString("\n")
		body.WriteString(subtleStyle.Render(fmt.Sprintf("Add it to your PATH:\n  export PATH=\"%s:$PATH\"", prefix)))
	}
	fmt.Println(boxStyle.Render(body.String()))
}
