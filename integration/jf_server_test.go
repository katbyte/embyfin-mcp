//go:build integration

package integration

import (
	"io"
	"slices"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/jf"
	"github.com/katbyte/go-kt/pointer"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestJFSystem(t *testing.T) {
	ctx := skipUnlessJellyfin(t)

	// (OperatingSystem is empty on this server; the paths and ids are not)
	info := must(jfc.GetSystemInfo(ctx)).Model
	if info.Id == "" || info.Version == "" || info.ServerName == "" || info.LogPath == "" || info.ProgramDataPath == "" || info.LocalAddress == "" {
		t.Errorf("GetSystemInfo = %+v", info)
	}
	if !pointer.From(info.StartupWizardCompleted) {
		t.Error("StartupWizardCompleted is false after testenv.sh completed the wizard")
	}
	pub := must(jfc.GetPublicSystemInfo(ctx)).Model
	if pub.Id != info.Id || pub.Version != info.Version || pub.ServerName == "" || pub.ProductName == "" {
		t.Errorf("GetPublicSystemInfo = %+v, want to match %+v", pub, info)
	}
	if ping := pointer.From(must(jfc.GetPingSystem(ctx)).Model); ping != "Jellyfin Server" {
		t.Errorf("GetPingSystem = %q", ping)
	}
	if ping := pointer.From(must(jfc.PostPingSystem(ctx)).Model); ping != "Jellyfin Server" {
		t.Errorf("PostPingSystem = %q", ping)
	}
	if ep := must(jfc.GetEndpointInfo(ctx)).Model; !pointer.From(ep.IsInNetwork) {
		t.Errorf("GetEndpointInfo = %+v, want IsInNetwork from the docker bridge", ep)
	}
	// the storage report is newer than the spec's server: a 404 today, a
	// populated report once the image catches up
	if storage, err := jfc.GetSystemStorage(ctx); err != nil {
		if !client.IsNotFound(err) {
			t.Errorf("GetSystemStorage = %v", err)
		}
	} else if storage.Model.ProgramDataFolder == nil || storage.Model.ProgramDataFolder.Path == "" {
		t.Errorf("GetSystemStorage = %+v", storage.Model)
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestJFLogsAndActivity(t *testing.T) {
	ctx := skipUnlessJellyfin(t)
	jfLibrary(t, sdkMovies) // so there is activity to list

	logs := must(jfc.GetServerLogs(ctx)).Model
	if len(logs) == 0 {
		t.Fatal("GetServerLogs listed nothing")
	}
	if logs[0].Name == "" || logs[0].Size == 0 || logs[0].DateModified == "" {
		t.Errorf("log file = %+v", logs[0])
	}
	body := must(jfc.GetLogFile(ctx, jf.GetLogFileOperationOptions{Name: logs[0].Name})).HttpResponse.Body
	defer func() { _ = body.Close() }()
	if n, err := io.Copy(io.Discard, body); err != nil || n == 0 {
		t.Errorf("GetLogFile(%s) read %d bytes, err %v", logs[0].Name, n, err)
	}

	entries := must(jfc.GetLogEntries(ctx, jf.GetLogEntriesOperationOptions{Limit: new(5)})).Model
	if entries.TotalRecordCount == 0 || len(entries.Items) == 0 {
		t.Fatalf("GetLogEntries = %+v", entries)
	}
	for _, e := range entries.Items {
		if e.Id == 0 || e.Name == "" || e.Type == "" || e.Date == "" || e.Severity == "" {
			t.Errorf("activity entry did not decode: %+v", e)
		}
	}

	devices := must(jfc.GetDevices(ctx, jf.GetDevicesOperationOptions{})).Model
	if devices.TotalRecordCount == 0 || len(devices.Items) == 0 {
		t.Fatalf("GetDevices = %+v", devices)
	}
	// a device is listed once a user has authenticated from it, which an
	// API key never does; the testenv script's login is always there
	if !slices.ContainsFunc(devices.Items, func(d jf.DeviceInfoDto) bool {
		return d.Id == "testenv" && d.AppName != "" && d.LastUserId == adminID && d.DateLastActivity != ""
	}) {
		t.Errorf("GetDevices does not list the testenv device: %+v", devices.Items)
	}
}

// The configuration, plugin and localisation reads a bare server answers.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestJFConfiguration(t *testing.T) {
	ctx := skipUnlessJellyfin(t)

	// (ServerName and MetadataPath are empty in the configuration when the
	// defaults are in force; the wizard flag and the plugin repository list
	// are what a fresh server fills in)
	cfg := must(jfc.GetConfiguration(ctx)).Model
	if !pointer.From(cfg.IsStartupWizardCompleted) || len(cfg.PluginRepositories) == 0 || cfg.LogFileRetentionDays == 0 {
		t.Errorf("GetConfiguration = %+v", cfg)
	}
	if raw := must(jfc.GetNamedConfiguration(ctx, "encoding")).Model; len(raw) < 2 {
		t.Errorf("GetNamedConfiguration(encoding) = %s", raw)
	}
	if opts := must(jfc.GetDefaultMetadataOptions(ctx)).Model; opts.ItemType != "" {
		t.Errorf("GetDefaultMetadataOptions = %+v, want the blank defaults", opts)
	}
	if _, err := jfc.GetBrandingOptions(ctx); err != nil {
		t.Errorf("GetBrandingOptions: %v", err)
	}
	css := must(jfc.GetBrandingCss(ctx))
	_ = css.HttpResponse.Body.Close()

	plugins := must(jfc.GetPlugins(ctx)).Model
	if !slices.ContainsFunc(plugins, func(p jf.PluginInfo) bool {
		return p.Name == "TMDb" && p.Id != "" && p.Version != "" && p.Status == jf.PluginStatusActive
	}) {
		t.Errorf("GetPlugins = %+v, want the bundled TMDb plugin active", plugins)
	}
	if repos := must(jfc.GetRepositories(ctx)).Model; len(repos) == 0 || repos[0].Url == "" {
		t.Errorf("GetRepositories = %+v", repos)
	}
	// the package catalogue is fetched from the repository, through the proxy
	packages := must(jfc.GetPackages(ctx)).Model
	if len(packages) == 0 || packages[0].Name == "" || len(packages[0].Versions) == 0 {
		t.Errorf("GetPackages = %d packages", len(packages))
	}

	cultures := must(jfc.GetCultures(ctx)).Model
	if !slices.ContainsFunc(cultures, func(c jf.CultureDto) bool {
		return c.TwoLetterISOLanguageName == "en" && c.ThreeLetterISOLanguageName == "eng" && c.DisplayName != ""
	}) {
		t.Errorf("GetCultures has no English among %d", len(cultures))
	}
	countries := must(jfc.GetCountries(ctx)).Model
	if !slices.ContainsFunc(countries, func(c jf.CountryInfo) bool {
		return c.TwoLetterISORegionName == "US" && c.ThreeLetterISORegionName == "USA"
	}) {
		t.Errorf("GetCountries has no US among %d", len(countries))
	}
	if ratings := must(jfc.GetParentalRatings(ctx)).Model; len(ratings) == 0 || ratings[0].Name == "" {
		t.Errorf("GetParentalRatings = %+v", ratings)
	}
	if opts := must(jfc.GetLocalizationOptions(ctx)).Model; !slices.ContainsFunc(opts, func(o jf.LocalizationOption) bool { return o.Value == "en-US" }) {
		t.Errorf("GetLocalizationOptions has no en-US among %d", len(opts))
	}

	// the default browser path is empty on Linux; the call has to answer
	if _, err := jfc.GetDefaultDirectoryBrowser(ctx); err != nil {
		t.Errorf("GetDefaultDirectoryBrowser: %v", err)
	}
	contents := must(jfc.GetDirectoryContents(ctx, jf.GetDirectoryContentsOperationOptions{Path: "/media", IncludeDirectories: new(true), IncludeFiles: new(false)})).Model
	if !slices.ContainsFunc(contents, func(e jf.FileSystemEntryInfo) bool {
		return e.Path == "/media/movies" && e.Type == jf.FileSystemEntryTypeDirectory
	}) {
		t.Errorf("GetDirectoryContents(/media) = %+v", contents)
	}
	if drives := must(jfc.GetDrives(ctx)).Model; len(drives) == 0 {
		t.Error("GetDrives listed nothing")
	}
	if parent := pointer.From(must(jfc.GetParentPath(ctx, jf.GetParentPathOperationOptions{Path: "/media/movies"})).Model); parent != "/media" {
		t.Errorf("GetParentPath(/media/movies) = %q", parent)
	}
	if _, err := jfc.ValidatePath(ctx, jf.ValidatePathDto{Path: "/media/movies"}); err != nil {
		t.Errorf("ValidatePath(/media/movies): %v", err)
	}
	if _, err := jfc.ValidatePath(ctx, jf.ValidatePathDto{Path: "/media/does-not-exist"}); client.StatusCode(err) != 404 {
		t.Errorf("ValidatePath on a missing folder = %v, want a 404", err)
	}

	prefs := must(jfc.GetDisplayPreferences(ctx, "usersettings", jf.GetDisplayPreferencesOperationOptions{UserId: adminID, Client: "emby"})).Model
	if prefs.Id == "" || prefs.Client != "emby" {
		t.Errorf("GetDisplayPreferences = %+v", prefs)
	}

	// the wizard is done: its endpoints refuse an ordinary caller. The
	// admin key is elevated, so the read still answers; assert the shape.
	if cfg := must(jfc.GetStartupConfiguration(ctx)).Model; cfg.UICulture == "" || cfg.MetadataCountryCode == "" {
		t.Errorf("GetStartupConfiguration = %+v", cfg)
	}

	// the Jellyfin-only families a bare server can answer. Quick connect is
	// on by default now, so initiating hands back a code; off, it is a 401.
	if pointer.From(must(jfc.GetQuickConnectEnabled(ctx)).Model) {
		if qc := must(jfc.InitiateQuickConnect(ctx)).Model; qc.Secret == "" || qc.Code == "" || pointer.From(qc.Authenticated) {
			t.Errorf("InitiateQuickConnect = %+v", qc)
		}
	} else if _, err := jfc.InitiateQuickConnect(ctx); client.StatusCode(err) != 401 {
		t.Errorf("InitiateQuickConnect with quick connect off = %v, want a 401", err)
	}
	if backups, err := jfc.ListBackups(ctx); err != nil || len(backups.Model) != 0 {
		t.Errorf("ListBackups = %+v, %v", backups.Model, err)
	}
	// live tv is "enabled" with the built-in service and no tuner behind it
	tv := must(jfc.GetLiveTvInfo(ctx)).Model
	if len(tv.Services) == 0 || tv.Services[0].Name == "" || len(tv.Services[0].Tuners) != 0 {
		t.Errorf("GetLiveTvInfo = %+v on a server with no tuner", tv)
	}
	if types := must(jfc.GetTunerHostTypes(ctx)).Model; len(types) == 0 || types[0].Id == "" {
		t.Errorf("GetTunerHostTypes = %+v", types)
	}
	if _, err := jfc.GetGuideInfo(ctx); err != nil {
		t.Errorf("GetGuideInfo: %v", err)
	}
}
