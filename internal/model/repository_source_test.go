package model

import "testing"

func TestRepositoryURLInputAndIdentity(t *testing.T) {
	for _, value := range []string{
		"https://github.com/acme/widgets.git", "git@github.com:acme/widgets.git",
		"ssh://git@example.test:2222/team/widgets.git", "git://example.test/team/widgets",
		"file:///home/user/code/widgets", "server:team/widgets",
	} {
		if err := ValidateRepositoryURL(value); err != nil {
			t.Errorf("valid URL %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "widgets", "/home/user/code", "https://", "https://example.test/", "https://example.test/widgets?token=x", "ext::false", "file://remote/path", "ftp://example.test/widgets", "ssh://example.test/a\nb"} {
		if err := ValidateRepositoryURL(value); err == nil {
			t.Errorf("invalid repository input accepted: %q", value)
		}
	}
	repository := Repository{SourcePath: "/home/user/code/widgets", Remotes: []RepositoryRemote{
		{Name: "origin", FetchURL: "git@github.com:Acme/Widgets.git"},
		{Name: "upstream", FetchURL: "ssh://git@example.test:2222/team/widgets.git"},
	}}
	for _, value := range []string{"https://github.com/acme/widgets/", "ssh://git@github.com/ACME/widgets.git", "file:///home/user/code/widgets", "ssh://git@example.test:2222/team/widgets.git"} {
		if !repository.HasSourceURL(value) {
			t.Errorf("source lookup did not find %q", value)
		}
	}
	for _, value := range []string{"https://notgithub.com/acme/widgets", "https://github.com/acme/other", "https://example.test/team/widgets.git", "ssh://other@example.test:2222/team/widgets.git", "file:///home/user/code/widgets.git"} {
		if repository.HasSourceURL(value) {
			t.Errorf("source lookup selected another repository for %q", value)
		}
	}
}
