package tools

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/oauth2/google"
	googleoption "google.golang.org/api/option"
	"google.golang.org/api/people/v1"
)

// peopleScopes はPeople API連携に必要なOAuthスコープです。いずれも参照専用です。
// directory.readonly が社内ディレクトリ、contacts.readonly が個人の連絡先に対応します。
var peopleScopes = []string{
	people.DirectoryReadonlyScope,
	people.ContactsReadonlyScope,
}

const (
	// peopleReadMask は取得するフィールド。指定は必須で、指定していないフィールドは返りません。
	peopleReadMask = "names,emailAddresses,organizations,phoneNumbers"
	// directorySourceProfile はWorkspaceのユーザープロフィール、
	// directorySourceContact はドメイン共有の連絡先です。
	directorySourceProfile = "DIRECTORY_SOURCE_TYPE_DOMAIN_PROFILE"
	directorySourceContact = "DIRECTORY_SOURCE_TYPE_DOMAIN_CONTACT"
)

// RealPeopleClient はGoogle Workspaceサービスアカウント + ドメイン委任でPeople APIを呼び出す実装です。
// 認証情報はGmail/Drive/カレンダー/スプレッドシート連携と共通です。
type RealPeopleClient struct {
	service *people.Service
}

// NewRealPeopleClient はサービスアカウント鍵JSONと代理対象ユーザーのメールアドレスからPeopleClientを構築します。
func NewRealPeopleClient(ctx context.Context, serviceAccountJSON, impersonatedUser string) (*RealPeopleClient, error) {
	cfg, err := google.JWTConfigFromJSON([]byte(serviceAccountJSON), peopleScopes...)
	if err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	cfg.Subject = impersonatedUser

	svc, err := people.NewService(ctx, googleoption.WithHTTPClient(cfg.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create people service: %w", err)
	}
	return &RealPeopleClient{service: svc}, nil
}

func (c *RealPeopleClient) SearchDirectory(ctx context.Context, query string, limit int) ([]DirectoryPerson, error) {
	resp, err := c.service.People.SearchDirectoryPeople().
		Query(query).
		ReadMask(peopleReadMask).
		Sources(directorySourceProfile, directorySourceContact).
		PageSize(int64(normalizePeopleLimit(limit))).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("search directory (%q): %w", query, err)
	}

	result := make([]DirectoryPerson, 0, len(resp.People))
	for _, p := range resp.People {
		result = append(result, toDirectoryPerson(p))
	}
	return result, nil
}

func (c *RealPeopleClient) SearchContacts(ctx context.Context, query string, limit int) ([]DirectoryPerson, error) {
	resp, err := c.service.People.SearchContacts().
		Query(query).
		ReadMask(peopleReadMask).
		PageSize(int64(normalizePeopleLimit(limit))).
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("search contacts (%q): %w", query, err)
	}

	result := make([]DirectoryPerson, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.Person == nil {
			continue
		}
		result = append(result, toDirectoryPerson(r.Person))
	}
	return result, nil
}

// toDirectoryPerson はPeople APIのPersonを表示用の形へ変換します。
// 各フィールドは複数返ることがあるため、先頭(=主要なもの)だけを採用します。
func toDirectoryPerson(p *people.Person) DirectoryPerson {
	var person DirectoryPerson
	if len(p.Names) > 0 {
		person.Name = p.Names[0].DisplayName
	}
	if len(p.EmailAddresses) > 0 {
		person.Email = p.EmailAddresses[0].Value
	}
	if len(p.PhoneNumbers) > 0 {
		person.Phone = p.PhoneNumbers[0].Value
	}
	if len(p.Organizations) > 0 {
		org := p.Organizations[0]
		// 部署と役職はどちらか片方しか入っていないことがあるため、あるものだけを繋ぐ。
		parts := make([]string, 0, 3)
		for _, v := range []string{org.Name, org.Department, org.Title} {
			if v != "" {
				parts = append(parts, v)
			}
		}
		person.Organization = strings.Join(parts, " / ")
	}
	return person
}
