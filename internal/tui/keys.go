package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Switch  key.Binding // enter on the list
	Rename  key.Binding
	Remove  key.Binding
	Quit    key.Binding
	Confirm key.Binding // y in dialogs
	Cancel  key.Binding // n/esc in dialogs
	Accept  key.Binding // enter in the rename input
	// CancelInput leaves the rename input. It must not share the dialogs'
	// Cancel binding: there "n" means no, here it is a letter being typed.
	CancelInput key.Binding
}

var keys = keyMap{
	Switch:      key.NewBinding(key.WithKeys("enter")),
	Rename:      key.NewBinding(key.WithKeys("r")),
	Remove:      key.NewBinding(key.WithKeys("d")),
	Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c")),
	Confirm:     key.NewBinding(key.WithKeys("y", "Y")),
	Cancel:      key.NewBinding(key.WithKeys("n", "N", "esc")),
	Accept:      key.NewBinding(key.WithKeys("enter")),
	CancelInput: key.NewBinding(key.WithKeys("esc", "ctrl+c")),
}
