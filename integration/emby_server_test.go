//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"slices"
	"testing"

	"github.com/katbyte/embyfin-mcp/lib/client"
	"github.com/katbyte/embyfin-mcp/lib/emby"
	"github.com/katbyte/go-kt/pointer"
)

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbySystem(t *testing.T) {
	ctx := skipUnlessEmby(t)

	// (LogPath and ProgramDataPath are null on this server)
	info := must(embyc.GetSystemInfo(ctx)).Model
	if info.Id == "" || info.Version == "" || info.ServerName == "" || info.OperatingSystem == "" || info.HttpServerPortNumber == 0 {
		t.Errorf("GetSystemInfo = %+v", info)
	}
	pub := must(embyc.GetSystemInfoPublic(ctx)).Model
	if pub.Id != info.Id || pub.Version != info.Version || pub.ServerName == "" {
		t.Errorf("GetSystemInfoPublic = %+v, want to match %+v", pub, info)
	}
	ping := must(embyc.GetSystemPing(ctx))
	defer func() { _ = ping.HttpResponse.Body.Close() }()
	if body, err := io.ReadAll(ping.HttpResponse.Body); err != nil || string(body) != "Emby Server" {
		t.Errorf("GetSystemPing = %q, %v", body, err)
	}
	if ep := must(embyc.GetSystemEndpoint(ctx)).Model; !pointer.From(ep.IsInNetwork) {
		t.Errorf("GetSystemEndpoint = %+v, want IsInNetwork from the docker bridge", ep)
	}
	if _, err := embyc.GetSystemWakeOnLanInfo(ctx); err != nil {
		t.Errorf("GetSystemWakeOnLanInfo: %v", err)
	}
}

//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyLogsAndActivity(t *testing.T) {
	ctx := skipUnlessEmby(t)
	embyLibrary(t, sdkMovies) // so there is activity to list

	logs := must(embyc.GetSystemLogsQuery(ctx, emby.GetSystemLogsQueryOperationOptions{})).Model.Items
	if len(logs) == 0 {
		t.Fatal("GetSystemLogsQuery listed nothing")
	}
	if logs[0].Name == "" || logs[0].Size == 0 || logs[0].DateModified == "" {
		t.Errorf("log file = %+v", logs[0])
	}
	body := must(embyc.GetSystemLogsByName(ctx, logs[0].Name, emby.GetSystemLogsByNameOperationOptions{})).HttpResponse.Body
	defer func() { _ = body.Close() }()
	if n, err := io.Copy(io.Discard, body); err != nil || n == 0 {
		t.Errorf("GetSystemLogsByName(%s) read %d bytes, err %v", logs[0].Name, n, err)
	}

	entries := must(embyc.GetSystemActivityLogEntries(ctx, emby.GetSystemActivityLogEntriesOperationOptions{Limit: 5})).Model
	if entries.TotalRecordCount == 0 || len(entries.Items) == 0 {
		t.Fatalf("GetSystemActivityLogEntries = %+v", entries)
	}
	for _, e := range entries.Items {
		if e.Id == 0 || e.Name == "" || e.Type == "" || e.Date == "" || e.Severity == "" {
			t.Errorf("activity entry did not decode: %+v", e)
		}
	}

	// (TotalRecordCount is 0 whatever the list holds; a device is listed
	// once a user has authenticated from it, which an API key never does,
	// and the testenv script's login is always there)
	devices := must(embyc.GetDevices(ctx, emby.GetDevicesOperationOptions{})).Model
	if !slices.ContainsFunc(devices.Items, func(d emby.DevicesDeviceInfo) bool {
		return d.Name == "testenv" && d.Id != "" && d.AppName != "" && d.LastUserId == adminID && d.DateLastActivity != ""
	}) {
		t.Errorf("GetDevices does not list the testenv device: %+v", devices.Items)
	}
}

// The configuration, plugin and localisation reads a bare server answers.
//
//nolint:paralleltest // the tests share one server and its libraries
func TestEmbyConfiguration(t *testing.T) {
	ctx := skipUnlessEmby(t)

	// (ServerName and MetadataPath are null while the defaults are in force)
	cfg := must(embyc.GetSystemConfiguration(ctx)).Model
	if !pointer.From(cfg.IsStartupWizardCompleted) || cfg.UICulture == "" || cfg.HttpServerPortNumber == 0 || cfg.LogFileRetentionDays == 0 {
		t.Errorf("GetSystemConfiguration = %+v", cfg)
	}
	raw := must(embyc.GetSystemConfigurationByKey(ctx, "encoding")).Model
	var encoding map[string]any
	if err := json.Unmarshal(raw, &encoding); err != nil || len(encoding) == 0 {
		t.Errorf("GetSystemConfigurationByKey(encoding) = %s, %v", raw, err)
	}
	if _, err := embyc.GetBrandingConfiguration(ctx); err != nil {
		t.Errorf("GetBrandingConfiguration: %v", err)
	}
	css := must(embyc.GetBrandingCss(ctx))
	_ = css.HttpResponse.Body.Close()

	plugins := must(embyc.GetPlugins(ctx)).Model
	if len(plugins) == 0 || plugins[0].Id == "" || plugins[0].Name == "" || plugins[0].Version == "" {
		t.Errorf("GetPlugins = %+v", plugins)
	}
	// the package catalogue is fetched from Emby's repository, through the
	// proxy; when the fetch fails the server answers 500 rather than an
	// empty list
	if packages, err := embyc.GetPackages(ctx, emby.GetPackagesOperationOptions{}); err != nil {
		if client.StatusCode(err) != 500 {
			t.Errorf("GetPackages = %v", err)
		}
		t.Logf("GetPackages: %v", err)
	} else if len(packages.Model) == 0 || packages.Model[0].Name == "" || packages.Model[0].Guid == "" {
		t.Errorf("GetPackages = %d packages", len(packages.Model))
	}

	cultures := must(embyc.GetLocalizationCultures(ctx)).Model
	if !slices.ContainsFunc(cultures, func(c emby.GlobalizationCultureDto) bool {
		return c.TwoLetterISOLanguageName == "en" && c.ThreeLetterISOLanguageName == "eng" && c.DisplayName != ""
	}) {
		t.Errorf("GetLocalizationCultures has no English among %d", len(cultures))
	}
	countries := must(embyc.GetLocalizationCountries(ctx)).Model
	if !slices.ContainsFunc(countries, func(c emby.GlobalizationCountryInfo) bool {
		return c.TwoLetterISORegionName == "US" && c.ThreeLetterISORegionName == "USA"
	}) {
		t.Errorf("GetLocalizationCountries has no US among %d", len(countries))
	}
	if ratings := must(embyc.GetLocalizationParentalRatings(ctx)).Model; len(ratings) == 0 || ratings[0].Name == "" {
		t.Errorf("GetLocalizationParentalRatings = %+v", ratings)
	}
	if opts := must(embyc.GetLocalizationOptions(ctx)).Model; !slices.ContainsFunc(opts, func(o emby.GlobalizationLocalizatonOption) bool { return o.Value == "en-US" }) {
		t.Errorf("GetLocalizationOptions has no en-US among %d", len(opts))
	}

	// the default browser path is empty on Linux; the call has to answer
	if _, err := embyc.GetEnvironmentDefaultDirectoryBrowser(ctx); err != nil {
		t.Errorf("GetEnvironmentDefaultDirectoryBrowser: %v", err)
	}
	contents := must(embyc.GetEnvironmentDirectoryContents(ctx, emby.GetEnvironmentDirectoryContentsOperationOptions{Path: "/media", IncludeDirectories: new(true), IncludeFiles: new(false)})).Model
	if !slices.ContainsFunc(contents, func(e emby.IOFileSystemEntryInfo) bool { return e.Path == "/media/movies" && e.Type == "Directory" }) {
		t.Errorf("GetEnvironmentDirectoryContents(/media) = %+v", contents)
	}
	if drives := must(embyc.GetEnvironmentDrives(ctx)).Model; len(drives) == 0 {
		t.Error("GetEnvironmentDrives listed nothing")
	}
	// /Environment/ParentPath answers the bare path as text, not the JSON
	// string the spec promises (the emby-parent-path-text workaround)
	parent := must(embyc.GetEnvironmentParentPath(ctx, emby.GetEnvironmentParentPathOperationOptions{Path: "/media/movies"}))
	defer func() { _ = parent.HttpResponse.Body.Close() }()
	if text, err := io.ReadAll(parent.HttpResponse.Body); err != nil || string(text) != "/media" {
		t.Errorf("GetEnvironmentParentPath(/media/movies) = %q, %v", text, err)
	}

	prefs := must(embyc.GetDisplayPreferencesById(ctx, "usersettings", emby.GetDisplayPreferencesByIdOperationOptions{UserId: adminID, Client: "emby"})).Model
	if prefs.Id == "" {
		t.Errorf("GetDisplayPreferencesById = %+v", prefs)
	}

	// the Emby-only families a bare server can answer
	// (the server sends Name, Id and Events per category; the spec's Type
	// and Category are not among them)
	if types := must(embyc.GetNotificationsTypes(ctx)).Model; len(types) == 0 || types[0].Name == "" {
		t.Errorf("GetNotificationsTypes = %+v", types)
	}
	if providers := must(embyc.GetAuthProviders(ctx)).Model; len(providers) == 0 || providers[0].Id == "" {
		t.Errorf("GetAuthProviders = %+v", providers)
	}
	// the built-in folder targets are always offered
	if targets := must(embyc.GetSyncTargets(ctx, emby.GetSyncTargetsOperationOptions{UserId: adminID})).Model; len(targets) == 0 || targets[0].Id == "" || targets[0].Name == "" {
		t.Errorf("GetSyncTargets = %+v", targets)
	}
	if jobs := must(embyc.GetSyncJobs(ctx)).Model; jobs.TotalRecordCount != 0 {
		t.Errorf("GetSyncJobs = %+v", jobs)
	}
	// Emby Connect is not linked: pending invitations are none
	if _, err := embyc.GetConnectPending(ctx); err != nil {
		t.Errorf("GetConnectPending: %v", err)
	}
	tv := must(embyc.GetLiveTvInfo(ctx)).Model
	if pointer.From(tv.IsEnabled) {
		t.Errorf("GetLiveTvInfo = %+v on a server with no tuner", tv)
	}
	if types := must(embyc.GetLiveTvTunerHostsTypes(ctx)).Model; len(types) == 0 || types[0].Id == "" {
		t.Errorf("GetLiveTvTunerHostsTypes = %+v", types)
	}
	if _, err := embyc.GetLiveTvGuideInfo(ctx); err != nil {
		t.Errorf("GetLiveTvGuideInfo: %v", err)
	}
}
