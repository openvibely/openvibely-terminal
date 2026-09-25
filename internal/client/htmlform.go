package client

import (
	"strings"

	"golang.org/x/net/html"
)

type htmlSelectValueMode uint8

const (
	htmlSelectAttributeValue htmlSelectValueMode = iota
	htmlSelectSelectedOption
	htmlSelectFirstOptionFallback
)

type htmlFormValueOptions struct {
	selectValueMode htmlSelectValueMode
	trimInput       bool
	trimTextarea    bool
}

func isNamedHTMLControl(node *html.Node, name string, elements ...string) bool {
	if node == nil || attr(node, "name") != name {
		return false
	}
	if len(elements) == 0 {
		return true
	}
	for _, element := range elements {
		if node.Data == element {
			return true
		}
	}
	return false
}

func findNamedHTMLControl(root *html.Node, name string, elements ...string) *html.Node {
	return findNode(root, func(node *html.Node) bool {
		return isNamedHTMLControl(node, name, elements...)
	})
}

func selectedHTMLFormOption(selectNode *html.Node, mode htmlSelectValueMode) *html.Node {
	if selectNode == nil {
		return nil
	}
	options := findAll(selectNode, func(node *html.Node) bool { return node.Data == "option" })
	for _, option := range options {
		if hasHTMLAttr(option, "selected") {
			return option
		}
	}
	if mode == htmlSelectFirstOptionFallback && len(options) > 0 {
		return options[0]
	}
	return nil
}

func htmlFormControlValue(control *html.Node, options htmlFormValueOptions) string {
	if control == nil {
		return ""
	}
	switch control.Data {
	case "textarea":
		value := rawNodeText(control)
		if options.trimTextarea {
			return strings.TrimSpace(value)
		}
		return value
	case "select":
		if options.selectValueMode != htmlSelectAttributeValue {
			if option := selectedHTMLFormOption(control, options.selectValueMode); option != nil {
				return attr(option, "value")
			}
		}
	}
	value := attr(control, "value")
	if options.trimInput && control.Data == "input" {
		return strings.TrimSpace(value)
	}
	return value
}
