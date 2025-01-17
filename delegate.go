package chroma

import (
	"bytes"
	"fmt"
)

type delegatingLexer struct {
	root     Lexer
	language Lexer
}

// DelegatingLexer combines two lexers to handle the common case of a language embedded inside another, such as PHP
// inside HTML or PHP inside plain text.
//
// It takes two lexer as arguments: a root lexer and a language lexer.  First everything is scanned using the language
// lexer, which must return "Other" for unrecognised tokens. Then all "Other" tokens are lexed using the root lexer.
// Finally, these two sets of tokens are merged.
//
// The lexers from the template lexer package use this base lexer.
func DelegatingLexer(root Lexer, language Lexer) Lexer {
	return &delegatingLexer{
		root:     root,
		language: language,
	}
}

func (d *delegatingLexer) AnalyseText(text string) float32 {
	return d.root.AnalyseText(text)
}

func (d *delegatingLexer) SetAnalyser(analyser func(text string) float32) Lexer {
	d.root.SetAnalyser(analyser)
	return d
}

func (d *delegatingLexer) SetRegistry(r *LexerRegistry) Lexer {
	d.root.SetRegistry(r)
	d.language.SetRegistry(r)
	return d
}

func (d *delegatingLexer) Config() *Config {
	return d.language.Config()
}

// An insertion is the character range where language tokens should be inserted.
type insertion struct {
	start, end int
	tokens     []Token
}

func (d *delegatingLexer) Tokenise(options *TokeniseOptions, text string) (Iterator, error) { // nolint: gocognit
	tokeniseFn := func(lexer Lexer, options *TokeniseOptions, text string) ([]Token, OriginalLenIterator, error) {
		tokens, err := Tokenise(lexer, options, text)
		return tokens, OriginalLenIterator{}, err
	}

	tokenizeRootFn := func(options *TokeniseOptions, text string) (Iterator, OriginalLenIterator, error) {
		it, err := d.root.Tokenise(options, text)
		return it, OriginalLenIterator{}, err
	}

	tokens, _, err := d.tokenise(options, tokeniseFn, tokenizeRootFn, text)
	return tokens, err
}

func (d *delegatingLexer) TokeniseWithOriginalLen(options *TokeniseOptions, text string) (Iterator, OriginalLenIterator, error) { // nolint: gocognit
	tokeniseFn := TokeniseWithOriginalLen

	tokenizeRootFn := func(options *TokeniseOptions, text string) (Iterator, OriginalLenIterator, error) {
		lex, ok := d.root.(TokeniserWithOriginalLen)

		if !ok {
			err := fmt.Errorf("lexer does not support tokenizing with offsets")
			return nil, OriginalLenIterator{}, err
		}

		it, offsetIter, err := lex.TokeniseWithOriginalLen(options, text)
		return it, offsetIter, err
	}

	return d.tokenise(options, tokeniseFn, tokenizeRootFn, text)
}

type tokenizeWithOriginalLen func(lexer Lexer, options *TokeniseOptions, text string) ([]Token, OriginalLenIterator, error)
type tokenizeRootWithOriginalLen func(options *TokeniseOptions, text string) (Iterator, OriginalLenIterator, error)

func (d *delegatingLexer) tokenise(options *TokeniseOptions, tokeniseFn tokenizeWithOriginalLen, tokenizeRootFn tokenizeRootWithOriginalLen, text string) (Iterator, OriginalLenIterator, error) { // nolint: gocognit
	tokens, offsetIter, err := tokeniseFn(Coalesce(d.language), options, text)
	if err != nil {
		return nil, OriginalLenIterator{}, err
	}
	// Compute insertions and gather "Other" tokens.
	others := &bytes.Buffer{}
	insertions := []*insertion{}
	var insert *insertion
	offset := 0
	var last Token
	for _, t := range tokens {
		if t.Type == Other {
			if last != EOF && insert != nil && last.Type != Other {
				insert.end = offset
			}
			others.WriteString(t.Value)
		} else {
			if last == EOF || last.Type == Other {
				insert = &insertion{start: offset}
				insertions = append(insertions, insert)
			}
			insert.tokens = append(insert.tokens, t)
		}
		last = t
		offset += len(t.Value)
	}

	if len(insertions) == 0 {
		// No insertions, so just return the iterator from the root lexer
		return tokenizeRootFn(options, text)
	}

	// Lex the other tokens.
	rootTokens, err := Tokenise(Coalesce(d.root), options, others.String())
	if err != nil {
		return nil, OriginalLenIterator{}, err
	}

	// Interleave the two sets of tokens.
	var out []Token
	offset = 0 // Offset into text.
	tokenIndex := 0
	nextToken := func() Token {
		if tokenIndex >= len(rootTokens) {
			return EOF
		}
		t := rootTokens[tokenIndex]
		tokenIndex++
		return t
	}
	insertionIndex := 0
	nextInsertion := func() *insertion {
		if insertionIndex >= len(insertions) {
			return nil
		}
		i := insertions[insertionIndex]
		insertionIndex++
		return i
	}
	t := nextToken()
	i := nextInsertion()
	for t != EOF || i != nil {
		// fmt.Printf("%d->%d:%q   %d->%d:%q\n", offset, offset+len(t.Value), t.Value, i.start, i.end, Stringify(i.tokens...))
		if t == EOF || (i != nil && i.start < offset+len(t.Value)) {
			var l Token
			l, t = splitToken(t, i.start-offset)
			if l != EOF {
				out = append(out, l)
				offset += len(l.Value)
			}
			out = append(out, i.tokens...)
			offset += i.end - i.start
			if t == EOF {
				t = nextToken()
			}
			i = nextInsertion()
		} else {
			out = append(out, t)
			offset += len(t.Value)
			t = nextToken()
		}
	}
	return Literator(out...), offsetIter, nil
}

func splitToken(t Token, offset int) (l Token, r Token) {
	if t == EOF {
		return EOF, EOF
	}
	if offset == 0 {
		return EOF, t
	}
	if offset == len(t.Value) {
		return t, EOF
	}
	l = t.Clone()
	r = t.Clone()
	l.Value = l.Value[:offset]
	r.Value = r.Value[offset:]
	return
}
