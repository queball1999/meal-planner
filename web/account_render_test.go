package web

import (
	"strings"
	"testing"

	"goeat/db"
)

func TestAccountPageExecutes(t *testing.T) {
	out := renderPage(t, "account", pageData{
		AppName: "Go Eat",
		Page:    "account",
		User:    &db.User{ID: 1, Username: "quynn"},
		Data: accountPageData{
			Username: "quynn", PlanCount: 3, RecipeCount: 21, PantryCount: 8,
		},
	})
	for _, want := range []string{
		`Signed in as <strong>quynn</strong>`,
		`action="/account/password"`,
		`href="/account/export"`,
		`action="/account/reset"`,
		`action="/account/wipe"`,
		`Type <code>RESET</code>`,
		`Type <code>DELETE</code>`,
		`action="/auth/logout"`, // sign out lives here now
	} {
		if !strings.Contains(out, want) {
			t.Errorf("account page missing %q", want)
		}
	}
}

// The header points at the account page and no longer carries an inline
// sign-out form or the old search submit button.
func TestHeaderLinksToAccountNotLogout(t *testing.T) {
	out := renderPage(t, "account", pageData{
		AppName: "Go Eat",
		Page:    "account",
		User:    &db.User{ID: 1, Username: "quynn"},
		Data:    accountPageData{Username: "quynn"},
	})
	if !strings.Contains(out, `href="/account"`) {
		t.Errorf("header should link to /account")
	}
	if !strings.Contains(out, `data-popover="navMenu"`) {
		t.Errorf("hamburger should be a popover trigger")
	}
	if !strings.Contains(out, `id="searchPop"`) {
		t.Errorf("mobile search popover should be present")
	}
	if strings.Contains(out, `class="user-menu__logout"`) {
		t.Errorf("old inline logout form should be gone from the header")
	}
}
