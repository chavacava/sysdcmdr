package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func main() {
	defaultFilter := flag.String("filter", "", "unit filter to apply at startup")
	extraProperties := flag.String("props", "", "unit properties to display, set to 'all' to display all properties")
	flag.Parse()

	// Widget creation
	app := tview.NewApplication()
	filterTxtbox := tview.NewInputField().SetText(*defaultFilter)
	display := tview.NewTextView()
	statusBar := NewStatusBar()
	commander := Commander{statusBar}
	unitList := NewUnitList(&commander, *defaultFilter, *extraProperties)

	// Helper to handle the focus ammong widgets
	focusHandler := NewFocusHandler(append([]tview.Primitive{}, filterTxtbox, unitList, display), app)

	// Layout of widgets
	flex := tview.NewFlex()
	flex.AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(filterTxtbox, 3, 1, true).
		AddItem(unitList, 0, 6, false),
		0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(display, 0, 6, false).
			AddItem(statusBar, 5, 1, false),
			0, 3, false)

	// Visual settings for widgets
	filterTxtbox.SetFieldWidth(20).SetFieldBackgroundColor(tcell.ColorBlack).SetBorder(true).SetTitle(" Filter ").SetTitleAlign(tview.AlignLeft)
	unitList.SetBorder(true).SetTitle(" Units ").SetTitleAlign(tview.AlignLeft)
	display.SetScrollable(true).SetDynamicColors(true).SetBorder(true)

	// Define widgets logic
	filterTxtbox.SetDoneFunc(func(key tcell.Key) {
		unitList.Update(filterTxtbox.GetText())
		focusHandler.Next()
	})
	filterTxtbox.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEsc:
			filterTxtbox.SetText("")
			return nil
		}
		return event
	})
	unitList.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		display.SetTitle(" Properties ")
		display.SetText(unitList.CurrentUnit().Properties())
	})

	unitList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyF5:
			unitList.Refresh()
		case tcell.KeyRune:
			switch event.Rune() {
			case 'j':
				display.SetTitle(" Journal ")
				unit := unitList.CurrentUnit()
				display.SetText(fmt.Sprintf("Loading journal of %s...", unit.Name()))
				out, err := commander.Exec("journalctl", "-o short-iso _SYSTEMD_UNIT="+unit.Name())
				if err == nil {
					display.SetText(string(out))
					focusHandler.Next()
				}
				return nil
			case 's':
				unit := unitList.CurrentUnit()
				command := "start"
				if unit.IsRunning() {
					command = "stop"
				}
				commander.Exec("systemctl", command+" "+unit.Name())
				return nil
			case 'r':
				unit := unitList.CurrentUnit()
				command := "restart"
				commander.Exec("systemctl", command+" "+unit.Name())
				return nil
			}

		}

		return event
	})

	flex.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyTab:
			focusHandler.Next()
		case tcell.KeyBacktab:
			focusHandler.Previous()
		default:
			return event
		}

		return nil
	})

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyF4:
			focusHandler.SetTo(filterTxtbox)
		case tcell.KeyF10:
			app.Stop()
		}
		return event
	})

	// Launch the application
	if err := app.SetRoot(flex, true).SetFocus(focusHandler.Current()).EnableMouse(true).Run(); err != nil {
		panic(err)
	}
}

type FocusHandler struct {
	primitives []tview.Primitive
	app        *tview.Application
	i          int
}

func NewFocusHandler(primitives []tview.Primitive, app *tview.Application) FocusHandler {
	return FocusHandler{primitives, app, 0}
}

func (fh *FocusHandler) Current() tview.Primitive {
	return fh.primitives[fh.i]
}

func (fh *FocusHandler) Next() {
	fh.i = (fh.i + 1) % len(fh.primitives)
	fh.app.SetFocus(fh.primitives[fh.i])
}

func (fh *FocusHandler) SetTo(target tview.Primitive) {
	for i, p := range fh.primitives {
		if p == target {
			fh.app.SetFocus(target)
			fh.i = i
			return
		}
	}
}

func (fh *FocusHandler) Previous() {
	if fh.i == 0 {
		fh.i = len(fh.primitives)
	}
	fh.i = fh.i - 1

	fh.app.SetFocus(fh.primitives[fh.i])
}

type UnitList struct {
	*tview.List
	units           map[string]Unit
	commander       *Commander
	currentFilter   string
	propertiesQuery string
}

func NewUnitList(commander *Commander, filter string, extraProperties string) *UnitList {
	propertiesQuery := " --property=ActiveState,SubState,Names"
	switch {
	case extraProperties == "all":
		propertiesQuery = ""
	case extraProperties != "":
		propertiesQuery += "," + extraProperties
	}

	result := UnitList{
		tview.NewList().ShowSecondaryText(false),
		map[string]Unit{},
		commander,
		filter,
		propertiesQuery,
	}

	result.Refresh()
	return &result
}

func (l *UnitList) addUnit(unit Unit) {
	l.units[unit.Name()] = unit
	l.AddItem(unit.ColorizedString(), unit.Name(), 0, nil)
}

func (l *UnitList) CurrentUnit() Unit {
	_, secondary := l.GetItemText(l.GetCurrentItem())
	return l.units[secondary]
}

func (l *UnitList) Update(filter string) {
	l.currentFilter = filter
	l.Clear()
	filter = "*" + filter + "*"
	out, err := l.commander.Exec("systemctl", fmt.Sprintf("show%s -t service %s", l.propertiesQuery, filter))
	if err != nil {
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	block := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if block != "" {
				l.addUnit(NewUnitFromShowOutputBlock(block))
			}
			block = ""
			continue
		}

		block += line + "\n"
	}
	if block != "" {
		l.addUnit(NewUnitFromShowOutputBlock(block))
	}
}

func (l *UnitList) Refresh() {
	l.Update(l.currentFilter)
}

type Unit struct {
	properties map[string]string
}

func NewUnitFromShowOutputBlock(output string) Unit {
	lines := strings.Split(output, "\n")
	properties := map[string]string{}
	for _, line := range lines {
		parts := strings.Split(line, "=")
		if len(parts) < 2 {
			continue
		}
		properties[parts[0]] = parts[1]
	}

	return Unit{properties}
}

func (u Unit) Name() string {
	return u.properties["Names"]
}

func (u Unit) IsRunning() bool {
	return u.properties["SubState"] == "running"
}

func (u Unit) Properties() string {
	result := ""
	keys := make([]string, 0, len(u.properties))
	for k := range u.properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		result += fmt.Sprintf("[green]%s[grey]=[yellow]%s\n", k, u.properties[k])
	}

	return result
}

func (u Unit) ColorizedString() string {
	result := ""
	color := "[grey]"
	if u.properties["ActiveState"] == "active" {
		color = "[yellow]"
		if u.properties["SubState"] == "running" {
			color = "[green]"
		}
	}

	result = color + u.Name()

	return result
}

type Commander struct {
	statusBar *StatusBar
}

func (c Commander) Exec(command string, args string) (out []byte, err error) {
	cmdArgs := strings.Split(args, " ")
	out, err = exec.Command(command, cmdArgs...).CombinedOutput()
	txt := command + " " + args + "\n"
	if err != nil {
		txt += string(out)
		c.statusBar.Error(txt)
		return
	}

	c.statusBar.Info(txt)

	return
}

type StatusBar struct {
	*tview.TextView
}

func NewStatusBar() *StatusBar {
	sb := StatusBar{tview.NewTextView()}
	sb.SetTitle(" Status ").SetBorder(true).SetTitleAlign(tview.AlignLeft)
	return &sb
}

func (sb StatusBar) Error(msg string) {
	sb.SetBackgroundColor(tcell.ColorRed)
	sb.SetTextColor(tcell.ColorWhite)
	sb.SetText(msg)
}

func (sb StatusBar) Info(msg string) {
	sb.SetBackgroundColor(tcell.ColorBlack)
	sb.SetTextColor(tcell.ColorLightGray)
	sb.SetText(msg)
}
