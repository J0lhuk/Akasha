package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"akasha/internal/auth"
	"akasha/internal/core"
	"akasha/internal/embedding"
	"akasha/internal/ilang"
	"akasha/internal/integration"
	"akasha/internal/resources"
	"akasha/internal/skillrepo"
	"akasha/internal/syncer"
	"akasha/internal/vault"
	"akasha/internal/webui"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.2.0"

func main() {
	resources.Configure()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Akasha:", err)
		os.Exit(1)
	}
}
func output(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func defaultData() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ".akasha"
	}
	return filepath.Join(base, "Akasha")
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Print(helpText)
		return nil
	}
	command := args[0]
	if command == "version" {
		fmt.Println("Akasha", version)
		return nil
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	data := f.String("data", defaultData(), "local data directory")
	project := f.String("project", "", "project id")
	space := f.String("space", "", "space id")
	name := f.String("name", "", "display name")
	root := f.String("root", "", "local source/group root")
	query := f.String("query", "", "search/task query")
	budget := f.Int("budget", 0, "context token budget")
	phase := f.String("phase", "implement", "context phase")
	shared := f.Bool("shared", true, "include readable group memory")
	kind := f.String("kind", "note", "memory kind")
	entity := f.String("entity", "", "logical entity")
	title := f.String("title", "", "title")
	text := f.String("text", "", "non-secret text")
	input := f.String("file", "", "input file")
	out := f.String("out", "", "output file/directory")
	id := f.String("id", "", "revision or new stable resource id")
	expected := f.String("expected", "", "comma-separated current head ids")
	subject := f.String("subject", "", "fact subject")
	predicate := f.String("predicate", "", "fact predicate")
	value := f.String("value", "", "fact value")
	reason := f.String("reason", "", "checkpoint/resolve/transfer reason")
	to := f.String("to", "", "destination project")
	role := f.String("role", "reader", "reader/editor/admin; empty revokes")
	user := f.String("user", "", "user id")
	listen := f.String("listen", "127.0.0.1:7341", "listen address")
	address := f.String("url", "", "HTTPS sync hub origin")
	tokenEnv := f.String("token-env", "", "local actor token environment variable")
	hubTokenEnv := f.String("hub-token-env", "AKASHA_HUB_TOKEN", "hub bearer token environment variable")
	hubTokenFile := f.String("hub-token-file", "", "local protected hub session credential")
	passEnv := f.String("pass-env", "AKASHA_VAULT_PASSWORD", "vault passphrase environment variable")
	transferEnv := f.String("transfer-pass-env", "AKASHA_KEY_PASSPHRASE", "key transfer passphrase environment variable")
	secretEnv := f.String("secret-env", "", "secret value environment variable")
	apply := f.Bool("apply", false, "apply restore; default is preview")
	gitBundle := f.Bool("git", false, "include encrypted Git bundle in capture")
	variables := f.String("variables", "", "JSON file with prompt template variables")
	label := f.String("label", "", "skill version label")
	description := f.String("description", "", "when to use a generated skill")
	replace := f.Bool("replace", false, "replace installed skill with backup")
	username := f.String("username", "", "account login name")
	databaseEnv := f.String("database-url-env", "AKASHA_DATABASE_URL", "PostgreSQL DSN environment variable for hub")
	disable := f.Bool("disable", false, "disable account when revoking sessions")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	abs, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	*data = abs
	hubCredential := os.Getenv(*hubTokenEnv)
	if *hubTokenFile != "" {
		hubCredential, err = auth.ReadCredential(*hubTokenFile)
		if err != nil {
			return err
		}
	}
	if command == "resources" {
		return output(map[string]any{"profile": resources.Default(), "vector_cache": core.VectorCacheStats()})
	}
	if command == "model-install" {
		v, e := embedding.Install(ctx, *data, func(s string) { fmt.Fprintln(os.Stderr, s) })
		if e != nil {
			return e
		}
		return output(v)
	}
	if command == "model-status" {
		v, e := embedding.Verify(*data)
		if e != nil {
			return e
		}
		return output(v)
	}
	if command == "embed" {
		engine, e := embedding.Open(*data, 2)
		if e != nil {
			return e
		}
		defer engine.Close()
		v, e := engine.Embed(ctx, []string{*text}, true)
		if e != nil {
			return e
		}
		return output(map[string]any{"model": embedding.ModelID, "dimensions": len(v[0]), "vector": v[0]})
	}
	if command == "tokens" {
		content, e := readTextInput(*input, *text)
		if e != nil {
			return e
		}
		n, e := core.Tokens(content)
		if e != nil {
			return e
		}
		return output(map[string]any{"tokens": n, "tokenizer": "o200k_base", "bytes": len(content)})
	}
	if command == "skill-installed" {
		roots := []string{*root}
		if *root == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return e
			}
			skillDir, e := integration.DefaultSkillDirectory()
			if e != nil {
				return e
			}
			roots = []string{filepath.Dir(skillDir), filepath.Join(home, ".agents", "skills")}
		}
		v, e := skillrepo.InstalledSkills(roots)
		if e != nil {
			return e
		}
		return output(v)
	}
	if command == "hub" {
		token := hubCredential
		var hub *syncer.Hub
		var err error
		if dsn := os.Getenv(*databaseEnv); dsn != "" {
			hub, err = syncer.NewPostgresHub(ctx, *data, token, dsn)
		} else {
			hub, err = syncer.NewHub(*data, token)
		}
		if err != nil {
			return err
		}
		defer hub.Close()
		if os.Getenv(*databaseEnv) != "" || os.Getenv("AKASHA_ADMIN_USERNAME") != "" {
			if err = hub.Manager.BootstrapAccount(ctx, token, os.Getenv("AKASHA_ADMIN_USERNAME"), os.Getenv("AKASHA_ADMIN_PASSWORD")); err != nil {
				return err
			}
		}
		return serve(ctx, *listen, hub, false)
	}
	manager, err := core.Open(*data)
	if err != nil {
		return err
	}
	defer manager.Close()
	token, err := auth.LocalToken(*data)
	if err != nil {
		return err
	}
	if err = manager.Bootstrap(token); err != nil {
		return err
	}
	if *tokenEnv != "" {
		token = os.Getenv(*tokenEnv)
	}
	actor, err := manager.Authenticate(token)
	if err != nil {
		return err
	}
	if command == "account-set" {
		target := *user
		if target == "" {
			target = actor.ID
		}
		if e := manager.SetAccount(ctx, actor, target, *username, os.Getenv(*passEnv)); e != nil {
			return e
		}
		return output(map[string]any{"updated": target, "sessions_revoked": true})
	}
	if command == "account-add" {
		v, e := manager.CreateAccount(ctx, actor, *name, *username, os.Getenv(*passEnv))
		if e != nil {
			return e
		}
		return output(v)
	}
	if command == "accounts" {
		v, e := manager.Accounts(actor)
		if e != nil {
			return e
		}
		return output(v)
	}
	if command == "sessions-revoke" {
		if e := manager.RevokeSessions(ctx, actor, *user, *disable); e != nil {
			return e
		}
		return output(map[string]bool{"revoked": true})
	}
	if command == "hub-login" {
		if *out == "" {
			return errors.New("--out is required for the session credential file")
		}
		if _, e := os.Lstat(*out); e == nil {
			return errors.New("session credential destination already exists; choose a new file")
		} else if !os.IsNotExist(e) {
			return e
		}
		v, e := syncer.Login(ctx, *address, *username, os.Getenv(*passEnv))
		if e != nil {
			return e
		}
		if e = auth.SaveCredential(*out, []byte(v.Token)); e != nil {
			return e
		}
		return output(map[string]any{"user": v.User, "expires": v.Expires, "credential_file": *out})
	}
	if command == "hub-account-add" || command == "hub-account-set" || command == "hub-sessions-revoke" || command == "hub-stats" {
		c, e := syncer.NewClient(*address, hubCredential)
		if e != nil {
			return e
		}
		var result any
		path := "/v1/accounts"
		method := "POST"
		var payload any = map[string]string{"name": *name, "username": *username, "password": os.Getenv(*passEnv)}
		if command == "hub-account-set" {
			payload = map[string]string{"user_id": *user, "username": *username, "password": os.Getenv(*passEnv)}
		}
		if command == "hub-sessions-revoke" {
			path = "/v1/sessions/revoke"
			payload = map[string]any{"user_id": *user, "disable": *disable}
		}
		if command == "hub-stats" {
			method = "GET"
			path = "/v1/stats"
			payload = nil
		}
		if e = c.Call(ctx, method, path, payload, &result); e != nil {
			return e
		}
		return output(result)
	}
	if command == "token" {
		if !actor.Admin {
			return core.ErrDenied
		}
		fmt.Println(token)
		return nil
	}
	if command == "init" {
		return output(map[string]string{"data": *data, "owner": actor.ID, "status": "ready"})
	}
	switch command {
	case "install-codex":
		if !actor.Admin {
			return core.ErrDenied
		}
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		destination := *out
		if destination == "" {
			destination, e = integration.DefaultSkillDirectory()
			if e != nil {
				return e
			}
		}
		if e = integration.InstallCommandSkill(destination, exe, *data); e != nil {
			return e
		}
		return output(map[string]string{"skill": destination, "command": "$akasha add", "activation": "select Akasha in skills; reopen the chat if the skill list has not refreshed"})
	case "add":
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		v, e := integration.Add(ctx, manager, actor, *root, *space, *name, exe, *data)
		if e != nil {
			return e
		}
		return output(v)
	case "serve":
		app := webui.New(manager)
		defer app.Close()
		maintenanceContext, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			app.Maintain(maintenanceContext, actor, func(e error) { fmt.Fprintln(os.Stderr, "maintenance:", e) })
		}()
		defer func() {
			stop()
			<-done
			for _, e := range manager.CheckpointDirty(context.Background()) {
				fmt.Fprintln(os.Stderr, "checkpoint:", e)
			}
		}()
		return serve(ctx, *listen, app, true)
	case "spaces":
		v, e := manager.Spaces(actor)
		if e != nil {
			return e
		}
		return output(v)
	case "space-add":
		v, e := manager.CreateSpace(actor, core.Space{ID: *id, Name: *name, Root: *root})
		if e != nil {
			return e
		}
		return output(v)
	case "projects":
		v, e := manager.Projects(actor, *space)
		if e != nil {
			return e
		}
		return output(v)
	case "project-add":
		v, e := manager.CreateProject(actor, core.Project{ID: *id, SpaceID: *space, Name: *name, Root: *root})
		if e != nil {
			return e
		}
		return output(v)
	case "users":
		v, e := manager.Users(actor)
		if e != nil {
			return e
		}
		return output(v)
	case "user-add":
		u, t, e := manager.CreateUser(actor, *name)
		if e != nil {
			return e
		}
		return output(map[string]any{"user": u, "token": t})
	case "grant":
		if e := manager.Grant(actor, core.Grant{UserID: *user, ProjectID: *project, Role: *role}); e != nil {
			return e
		}
		return output(map[string]bool{"ok": true})
	case "prompt-history":
		if *space == "" && *project == "" {
			j, e := manager.ProjectAt(actor, *root)
			if e != nil {
				return errors.New("project not connected; use akasha add or choose --space")
			}
			*project = j.ID
		}
		v, e := manager.PromptHistory(ctx, actor, *space, *project, 100)
		if e != nil {
			return e
		}
		return output(v)
	}
	client := func() (*syncer.Client, error) { return syncer.NewClient(*address, hubCredential) }
	if command == "skill-create" {
		if *project == "" {
			j, e := manager.ProjectAt(actor, *root)
			if e != nil {
				return e
			}
			*project = j.ID
		}
		j, e := manager.Project(actor, *project, "read")
		if e != nil {
			return e
		}
		if *space != "" && *space != j.SpaceID {
			return errors.New("generated skills stay in their source group; use explicit knowledge transfer for another group")
		}
		procedure, e := readTextInput(*input, *text)
		if e != nil {
			return e
		}
		result, e := skillrepo.Create(ctx, manager, actor, *project, skillrepo.CreateOptions{Name: *name, Description: *description, Procedure: procedure, Query: *query, Label: *label, Destination: *out})
		if e != nil {
			if result.Version.ID != "" {
				_ = output(result)
			}
			return e
		}
		defer func() {
			for _, e := range manager.CheckpointDirty(context.Background()) {
				fmt.Fprintln(os.Stderr, "checkpoint:", e)
			}
		}()
		if *address != "" {
			c, e := client()
			if e != nil {
				_ = output(result)
				return e
			}
			v, e := vault.Unlock(*data, []byte(os.Getenv(*passEnv)))
			if e != nil {
				_ = output(result)
				return e
			}
			defer v.Close()
			sharedID := core.SharedID(result.SpaceID)
			s, e := manager.Store(actor, sharedID, "write")
			if e != nil {
				return e
			}
			key, e := v.ProjectKey(s.Project)
			if e != nil {
				return e
			}
			sp, e := manager.Space(actor, result.SpaceID)
			if e != nil {
				return e
			}
			catalog, e := c.Catalog(ctx)
			if e != nil {
				_ = output(result)
				return e
			}
			found := false
			for _, entry := range catalog {
				if entry.Project.ID == sharedID {
					found = true
					break
				}
			}
			if !found {
				if e = c.Enroll(ctx, sp, s.Project); e != nil {
					_ = output(result)
					return e
				}
			}
			if _, e = c.Sync(ctx, s, key); e != nil {
				_ = output(result)
				return e
			}
			result.Cloud = "uploaded encrypted group repository"
		}
		return output(result)
	}
	if strings.HasPrefix(command, "skill-") {
		if *space == "" {
			j, e := manager.ProjectAt(actor, *root)
			if e != nil {
				return errors.New("skill repository requires --space or a connected current project")
			}
			*space = j.SpaceID
		}
		*project = core.SharedID(*space)
		if command == "skill-sync" || command == "skill-pull" {
			if command == "skill-sync" {
				c, e := client()
				if e != nil {
					return e
				}
				catalog, e := c.Catalog(ctx)
				if e != nil {
					return e
				}
				found := false
				for _, j := range catalog {
					if j.Project.ID == *project {
						found = true
					}
				}
				if !found {
					sp, e := manager.Space(actor, *space)
					if e != nil {
						return e
					}
					j, e := manager.Project(actor, *project, "admin")
					if e != nil {
						return e
					}
					if e = c.Enroll(ctx, sp, j); e != nil {
						return e
					}
				}
				command = "sync"
			} else {
				command = "sync-pull"
			}
		}
	}
	if command == "sync-catalog" || command == "sync-clone" || command == "hub-user-add" || command == "hub-grant" {
		c, e := client()
		if e != nil {
			return e
		}
		if command == "hub-user-add" {
			var result map[string]any
			if e = c.Call(ctx, "POST", "/v1/users", map[string]string{"name": *name}, &result); e != nil {
				return e
			}
			return output(result)
		}
		if command == "hub-grant" {
			if e = c.Call(ctx, "POST", "/v1/grants", core.Grant{UserID: *user, ProjectID: *project, Role: *role}, nil); e != nil {
				return e
			}
			return output(map[string]bool{"ok": true})
		}
		catalog, e := c.Catalog(ctx)
		if e != nil {
			return e
		}
		if command == "sync-catalog" {
			return output(catalog)
		}
		found := false
		for _, entry := range catalog {
			if entry.Project.ID == *project {
				found = true
				entry.Project.Root = *root
				if _, e = manager.Register(actor, entry.Space, entry.Project); e != nil {
					return e
				}
				break
			}
		}
		if !found {
			return core.ErrNotFound
		}
		if *input == "" {
			return errors.New("sync-clone requires --file with encrypted project key")
		}
	}
	if *project == "" {
		j, e := manager.ProjectAt(actor, *root)
		if e != nil {
			return errors.New("project not connected; use akasha add or --project")
		}
		*project = j.ID
	}
	access := integration.Access{Manager: manager, Principal: actor, Project: *project}
	write := false
	switch command {
	case "remember", "resolve", "import", "wiki-import", "checkpoint", "restore-checkpoint", "capture", "restore", "secret-set", "sync", "sync-pull", "sync-clone", "hook":
		write = true
	case "prompt-save", "cache-clear", "skill-publish", "vector-index", "ilang-enable", "ilang-disable", "ilang-define", "ilang-prepare", "ilang-answer", "ilang-report", "ilang-capture":
		write = true
	case "budget":
		write = *budget != 0
	}
	action := "read"
	if write {
		action = "write"
	}
	store, err := manager.Store(actor, *project, action)
	if err != nil {
		return err
	}
	if write {
		defer func() {
			for _, e := range manager.CheckpointDirty(context.Background()) {
				fmt.Fprintln(os.Stderr, "checkpoint:", e)
			}
		}()
	}
	var expectedIDs []string
	if *expected != "" {
		expectedIDs = strings.Split(*expected, ",")
	}
	if strings.HasPrefix(command, "ilang-") {
		return runIlang(ctx, ilang.Service{Manager: manager, Actor: actor, Project: *project}, command, *name, *id, *input)
	}
	switch command {
	case "vector-index":
		v, e := manager.IndexVectors(ctx, actor, *project)
		if e != nil {
			return e
		}
		return output(v)
	case "vector-status":
		v, e := store.VectorStatus(ctx)
		if e != nil {
			return e
		}
		return output(v)
	case "vector-search":
		v, e := manager.VectorSearch(ctx, actor, *project, *query, *shared, 20)
		if e != nil {
			return e
		}
		return output(v)
	case "skill-publish":
		v, changed, e := skillrepo.Publish(ctx, store, actor.ID, *input, *label)
		if e != nil {
			return e
		}
		return output(map[string]any{"version": v, "changed": changed, "cloud": "local publication; use skill-sync to upload"})
	case "skill-list", "skill-versions":
		v, e := skillrepo.Catalog(ctx, store, *name)
		if e != nil {
			return e
		}
		return output(v)
	case "skill-download", "skill-install":
		if command == "skill-download" && *out == "" {
			return errors.New("skill-download requires --out directory")
		}
		v, e := skillrepo.Install(ctx, store, *id, *out, *replace)
		if e != nil {
			return e
		}
		return output(v)
	case "status":
		rev, e := store.Revision(ctx)
		if e != nil {
			return e
		}
		policy, e := store.Budgets(ctx)
		if e != nil {
			return e
		}
		cache, e := store.CacheStats(ctx)
		if e != nil {
			return e
		}
		return output(map[string]any{"project": store.Project, "role": manager.Role(actor, *project), "revision": rev, "policy": policy, "cache": cache})
	case "budget":
		var v core.BudgetPolicy
		var e error
		if *budget == 0 {
			v, e = store.Budgets(ctx)
		} else {
			v, e = store.SetBudget(ctx, actor.ID, *phase, *budget)
		}
		if e != nil {
			return e
		}
		return output(v)
	case "cache":
		v, e := store.CacheStats(ctx)
		if e != nil {
			return e
		}
		return output(v)
	case "cache-clear":
		n, e := store.ClearCache(ctx)
		if e != nil {
			return e
		}
		return output(map[string]int64{"cleared": n})
	case "prompt-save":
		content, e := readTextInput(*input, *text)
		if e != nil {
			return e
		}
		r, changed, e := store.SavePrompt(ctx, actor.ID, *name, content, expectedIDs)
		if e != nil {
			return e
		}
		return output(map[string]any{"id": r.ID, "name": r.Title, "changed": changed})
	case "prompt-list":
		v, e := store.List(ctx, "prompt_template", 500)
		if e != nil {
			return e
		}
		items := []map[string]string{}
		for _, r := range v {
			items = append(items, map[string]string{"id": r.ID, "name": r.Title, "created": r.Created})
		}
		return output(items)
	case "prompt-get":
		r, e := store.Prompt(ctx, *name)
		if e != nil {
			return e
		}
		return output(r)
	case "prompt-render":
		vars := map[string]string{}
		if *variables != "" {
			b, e := readTextInput(*variables, "")
			if e != nil {
				return e
			}
			if e = json.Unmarshal([]byte(b), &vars); e != nil {
				return e
			}
		}
		r, e := store.RenderPrompt(ctx, *name, vars)
		if e != nil {
			return e
		}
		return output(r)
	case "search":
		v, e := manager.Search(ctx, actor, *project, *query, *shared, 20)
		if e != nil {
			return e
		}
		return output(v)
	case "context":
		v, e := manager.Context(ctx, actor, *project, core.ContextRequest{Query: *query, Budget: *budget, Phase: *phase, Shared: *shared})
		if e != nil {
			return e
		}
		return output(v)
	case "list":
		v, e := store.List(ctx, *kind, 500)
		if e != nil {
			return e
		}
		for i := range v {
			v[i].Blob = nil
		}
		return output(v)
	case "read":
		v, e := store.Get(ctx, *id)
		if e != nil {
			return e
		}
		if v.Kind == "secret" || v.Kind == "file" || v.Kind == "git_bundle" {
			return core.ErrDenied
		}
		return output(v)
	case "remember":
		if *input != "" {
			b, e := os.ReadFile(*input)
			if e != nil {
				return e
			}
			if len(b) > 2<<20 {
				return errors.New("input too large")
			}
			*text = string(b)
		}
		v, changed, e := store.Put(ctx, actor.ID, core.WriteInput{Entity: *entity, Kind: *kind, Title: *title, Text: *text, Expected: expectedIDs, Subject: *subject, Predicate: *predicate, Value: *value})
		if e != nil {
			return e
		}
		return output(map[string]any{"id": v.ID, "entity": v.Entity, "changed": changed})
	case "history":
		v, e := store.History(ctx, *entity)
		if e != nil {
			return e
		}
		for i := range v {
			v[i].Blob = nil
		}
		return output(v)
	case "graph":
		v, e := store.Graph(ctx)
		if e != nil {
			return e
		}
		return output(v)
	case "conflicts":
		v, e := store.Conflicts(ctx)
		if e != nil {
			return e
		}
		for i := range v {
			for j := range v[i].Versions {
				v[i].Versions[j].Blob = nil
			}
		}
		return output(v)
	case "resolve":
		v, e := store.Resolve(ctx, actor.ID, *entity, *id, *reason, expectedIDs)
		if e != nil {
			return e
		}
		v.Blob = nil
		return output(v)
	case "transfer":
		v, e := manager.Transfer(ctx, actor, *project, *to, *id, *reason)
		if e != nil {
			return e
		}
		return output(v)
	case "import":
		v, e := store.Import(ctx, actor.ID)
		if e != nil {
			return e
		}
		return output(v)
	case "wiki-export":
		v, e := store.ExportWiki(ctx)
		if e != nil {
			return e
		}
		return output(v)
	case "wiki-import":
		v, e := store.ImportWiki(ctx, actor.ID)
		if e != nil {
			return e
		}
		return output(v)
	case "checkpoint":
		v, e := store.Checkpoint(ctx, *reason)
		if e != nil {
			return e
		}
		return output(map[string]string{"path": v})
	case "restore-checkpoint":
		n, e := store.RestoreCheckpoint(ctx, *input)
		if e != nil {
			return e
		}
		return output(map[string]int{"restored": n})
	case "integration":
		if *out == "" {
			return errors.New("--out plugin directory required")
		}
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		if e = integration.Package(*out, exe, *data, *project); e != nil {
			return e
		}
		return output(map[string]string{"package": *out, "activation": "review plugin and trust hooks in Codex"})
	case "mcp":
		defer func() {
			if manager.Authorize(actor, *project, "write") == nil {
				for _, e := range manager.CheckpointDirty(context.Background()) {
					fmt.Fprintln(os.Stderr, "checkpoint:", e)
				}
			}
		}()
		maintenanceContext, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-maintenanceContext.Done():
					return
				case <-ticker.C:
					for _, e := range manager.CheckpointDirty(maintenanceContext) {
						fmt.Fprintln(os.Stderr, "checkpoint:", e)
					}
				}
			}
		}()
		defer func() { cancel(); <-done }()
		server := integration.NewMCP(access)
		return server.Run(ctx, &mcp.StdioTransport{})
	case "hook":
		b, e := io.ReadAll(io.LimitReader(os.Stdin, 2<<20+1))
		if e != nil {
			return e
		}
		if len(b) > 2<<20 {
			return errors.New("hook payload too large")
		}
		var in integration.HookInput
		if e = json.Unmarshal(b, &in); e != nil {
			return e
		}
		v, e := access.Hook(ctx, in, *budget)
		if e != nil {
			return e
		}
		return output(v)
	case "sync-enroll":
		c, e := client()
		if e != nil {
			return e
		}
		sp, e := manager.Space(actor, store.Project.SpaceID)
		if e != nil {
			return e
		}
		if e = c.Enroll(ctx, sp, store.Project); e != nil {
			return e
		}
		return output(map[string]bool{"enrolled": true})
	}
	switch command {
	case "capture", "restore", "git-export", "key-export", "key-import", "secret-set", "secret-get", "sync", "sync-pull", "sync-clone":
	default:
		return errors.New("unknown command: " + command)
	}
	if !actor.Admin {
		return core.ErrDenied
	}
	v, err := vault.Unlock(*data, []byte(os.Getenv(*passEnv)))
	if err != nil {
		return err
	}
	defer v.Close()
	if command == "key-import" || command == "sync-clone" {
		b, e := os.ReadFile(*input)
		if e != nil {
			return e
		}
		if e = v.Import(store.Project, []byte(os.Getenv(*transferEnv)), b); e != nil {
			return e
		}
		if command == "key-import" {
			return output(map[string]bool{"imported": true})
		}
	}
	if command == "key-export" {
		if *out == "" {
			return errors.New("--out encrypted key file required")
		}
		b, e := v.Export(store.Project, []byte(os.Getenv(*transferEnv)))
		if e != nil {
			return e
		}
		if e = core.AtomicWrite(*out, b, 0600); e != nil {
			return e
		}
		return output(map[string]string{"path": *out})
	}
	key, err := v.ProjectKey(store.Project)
	if err != nil {
		return err
	}
	defer clear(key[:])
	switch command {
	case "capture":
		report, e := syncer.Capture(ctx, store, actor.ID, key)
		if e != nil {
			return e
		}
		if *gitBundle {
			if _, e = syncer.CaptureGitBundle(ctx, store, actor.ID, key); e != nil {
				return e
			}
		}
		return output(report)
	case "restore":
		report, e := syncer.Restore(ctx, store, key, *apply)
		if e != nil {
			return e
		}
		return output(report)
	case "git-export":
		if *out == "" {
			return errors.New("--out bundle file required")
		}
		if e := syncer.ExportGitBundle(ctx, store, key, *out); e != nil {
			return e
		}
		return output(map[string]string{"path": *out})
	case "secret-set":
		if *secretEnv == "" {
			return errors.New("--secret-env required; do not put secrets in command arguments")
		}
		r, e := v.Put(ctx, store, actor.ID, *name, []byte(os.Getenv(*secretEnv)), expectedIDs)
		if e != nil {
			return e
		}
		return output(map[string]string{"id": r.ID, "name": r.Title})
	case "secret-get":
		if *out == "" {
			return errors.New("--out is required for explicitly requested secret export")
		}
		secret, e := v.Get(ctx, store, *id)
		if e != nil {
			return e
		}
		defer clear(secret)
		if e = core.AtomicWrite(*out, secret, 0600); e != nil {
			return e
		}
		return output(map[string]string{"path": *out})
	case "sync", "sync-pull", "sync-clone":
		c, e := client()
		if e != nil {
			return e
		}
		var report syncer.SyncReport
		if command == "sync" {
			report, e = c.Sync(ctx, store, key)
		} else {
			report, e = c.Pull(ctx, store, key)
		}
		if e != nil {
			return e
		}
		return output(report)
	}
	return errors.New("unhandled command")
}
func readTextInput(path, text string) (string, error) {
	if path == "" {
		if len(text) > 2<<20 {
			return "", errors.New("text exceeds 2 MiB")
		}
		return text, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 2<<20+1))
	if err != nil {
		return "", err
	}
	if len(b) > 2<<20 {
		return "", errors.New("text file exceeds 2 MiB")
	}
	return strings.TrimPrefix(string(b), "\ufeff"), nil
}

func serve(ctx context.Context, address string, handler http.Handler, localOnly bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if localOnly {
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("local UI must bind to loopback")
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	fmt.Fprintln(os.Stderr, "Akasha listening on", listener.Addr().String())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

const helpText = `Akasha — локальная память проектов для Codex (Go, SQLite, MCP)

  akasha init --data <directory>
  akasha install-codex
  akasha add --root <current project> [--space work]
  akasha status --root <current project>
  akasha budget --root <current project> --phase implement --budget 1600
  akasha serve --data <directory> --listen 127.0.0.1:7341
  akasha space-add --id work --name Работа --root <group directory>
  akasha project-add --space work --id app --name App --root <source directory>
  akasha import --project app
  akasha remember --project app --title Решение --text "Подтверждённый факт"
  akasha context --project app --query "текущая задача" --budget 1600
  akasha mcp --project app
  akasha integration --project app --out <new plugin directory>

Команды: init, install-codex, add, status, budget, tokens, cache, cache-clear,
prompt-save, prompt-list, prompt-get, prompt-render,
skill-installed, skill-publish, skill-list, skill-versions, skill-download,
skill-install, skill-sync, skill-pull, skill-create,
ilang-enable, ilang-disable, ilang-status, ilang-define, ilang-list,
ilang-prepare, ilang-answer, ilang-read, ilang-report, ilang-capture,
model-install, model-status, embed, vector-index, vector-status, vector-search,
resources, accounts, account-set, account-add, sessions-revoke, hub-login,
hub-account-add, hub-account-set, hub-sessions-revoke, hub-stats,
serve, token, spaces, space-add, projects, project-add, users, user-add,
grant, list, read, remember, search, context, graph, conflicts, resolve, transfer,
history, prompt-history, import, checkpoint, restore-checkpoint, wiki-export,
wiki-import, capture, restore, git-export, secret-set, secret-get, key-export,
key-import, sync-enroll, sync-catalog, sync-clone, sync, sync-pull, hub-user-add,
hub-grant, hub, mcp, hook, integration, version.

--data задаёт изолированный каталог. Пароли и cloud token поступают через
AKASHA_VAULT_PASSWORD, AKASHA_KEY_PASSPHRASE, AKASHA_HUB_TOKEN.
restore без --apply только показывает действия. См. README.md и docs/OPERATIONS.md.
`
