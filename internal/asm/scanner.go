package asm

import "strings"

// TokenKind classifies a scanned token.
type TokenKind int

const (
	TokenIdentifier TokenKind = iota
	TokenNumber
	TokenPunctuation
)

// Token is one lexical unit with its byte offset in the line.
type Token struct {
	Kind  TokenKind
	Value string
	Pos   int
}

// ScanLine tokenizes one assembly line. It stops at a comment
// (//, ##, ;, or # followed by whitespace) and returns the remaining
// text as the comment. It returns an error for unterminated strings
// and other lexical faults.
func ScanLine(line string) ([]Token, string, error) {
	var tokens []Token
	for i := 0; i < len(line); {
		switch ch := line[i]; {
		case isSpace(ch):
			i++
		case strings.HasPrefix(line[i:], "//"):
			return tokens, strings.TrimSpace(line[i+2:]), nil
		case strings.HasPrefix(line[i:], "##"):
			return tokens, strings.TrimSpace(line[i+2:]), nil
		case ch == ';':
			return tokens, strings.TrimSpace(line[i+1:]), nil
		case ch == '#' && i+1 < len(line) && isSpace(line[i+1]):
			return tokens, strings.TrimSpace(line[i+1:]), nil
		case ch == '"':
			// String literal (e.g. .ascii "foo"): scan to closing quote.
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(line) {
				return nil, "", &LexError{line: line, msg: "unterminated string literal"}
			}
			tokens = append(tokens, Token{Kind: TokenIdentifier, Value: line[i : j+1], Pos: i})
			i = j + 1
		case isPunctuation(ch):
			tokens = append(tokens, Token{Kind: TokenPunctuation, Value: string(ch), Pos: i})
			i++
		default:
			start := i
			for i < len(line) && isIdentChar(line[i]) {
				i++
			}
			val := line[start:i]
			if val == "" {
				i++
				continue
			}
			kind := TokenIdentifier
			if isNumberLiteral(val) {
				kind = TokenNumber
			}
			tokens = append(tokens, Token{Kind: kind, Value: val, Pos: start})
		}
	}
	return tokens, "", nil
}

// LexError reports a lexical fault on one input line.
type LexError struct {
	line string
	msg  string
}

func (e *LexError) Error() string { return "lex: " + e.msg + ": " + e.line }

func isSpace(ch byte) bool { return ch == ' ' || ch == '\t' || ch == '\r' }

func isIdentChar(ch byte) bool {
	if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
		return true
	}
	switch ch {
	case '_', '.', '$', '@':
		return true
	default:
		return false
	}
}

func isNumberLiteral(val string) bool {
	if len(val) == 0 {
		return false
	}
	if len(val) > 2 && val[0] == '0' && (val[1] == 'x' || val[1] == 'X') {
		for i := 2; i < len(val); i++ {
			if !isHexDigit(val[i]) {
				return false
			}
		}
		return true
	}
	if val[0] < '0' || val[0] > '9' {
		return false
	}
	// Integer or float literal (1.00000000, 1e-5).
	for i := 0; i < len(val); i++ {
		c := val[i]
		if c >= '0' && c <= '9' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func isHexDigit(ch byte) bool {
	return ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F'
}

func isPunctuation(ch byte) bool {
	switch ch {
	case ',', '[', ']', '(', ')', '+', '-', '*', '#', ':', '!':
		return true
	default:
		return false
	}
}
