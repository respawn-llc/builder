package testsetup

import (
	"fmt"
	"strings"

	"github.com/antlr4-go/antlr/v4"
	"github.com/tursodatabase/libsql-client-go/sqliteparser"
)

func SQLiteTokens(source string) ([]antlr.Token, error) {
	errors := &sqliteSyntaxErrors{}
	lexer := sqliteparser.NewSQLiteLexer(antlr.NewInputStream(source))
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(errors)
	rawTokens := lexer.GetAllTokens()
	tokens := make([]antlr.Token, 0, len(rawTokens))
	for _, token := range rawTokens {
		if token.GetChannel() == antlr.TokenDefaultChannel {
			tokens = append(tokens, token)
		}
	}
	return tokens, errors.err()
}

func ParseSQLite(source string) error {
	errors := &sqliteSyntaxErrors{}
	lexer := sqliteparser.NewSQLiteLexer(antlr.NewInputStream(source))
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(errors)
	parser := sqliteparser.NewSQLiteParser(antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel))
	parser.RemoveErrorListeners()
	parser.AddErrorListener(errors)
	document := parser.Parse()
	if err := errors.err(); err != nil {
		return err
	}
	for _, statements := range document.AllSql_stmt_list() {
		if len(statements.AllSql_stmt()) > 0 {
			return nil
		}
	}
	return fmt.Errorf("SQLite document contains no statement")
}

type sqliteSyntaxErrors struct {
	antlr.DefaultErrorListener
	messages []string
}

func (e *sqliteSyntaxErrors) SyntaxError(
	_ antlr.Recognizer,
	_ interface{},
	line int,
	column int,
	message string,
	_ antlr.RecognitionException,
) {
	e.messages = append(e.messages, fmt.Sprintf("%d:%d: %s", line, column, message))
}

func (e *sqliteSyntaxErrors) err() error {
	if len(e.messages) == 0 {
		return nil
	}
	return fmt.Errorf("parse SQLite: %s", strings.Join(e.messages, "; "))
}
