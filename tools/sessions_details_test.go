package tools

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A session says how what it plays reaches the device, and of a transcode
// what is re-encoded, into what, on what and why; details adds the file's
// own facts and every part of the transcode. Emby says of the decoder and
// the encoder whether each is hardware.
func TestSessionListSaysHowItPlays(t *testing.T) {
	t.Parallel()

	f, _ := zzyzxServer(t)
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []map[string]any{
			{
				"Id": "s1", "UserName": "Quux", "Client": "Zzyzx TV", "ApplicationVersion": "2.1.0", "DeviceName": "Lounge TV", "RemoteEndPoint": "10.9.9.12",
				"LastActivityDate": "2026-01-05T08:34:54.0000000Z",
				"NowPlayingItem":   map[string]any{"Id": "m1", "Name": "Zzyzx Rising", "Type": "Movie", "RunTimeTicks": 72_000_000_000},
				"PlayState":        map[string]any{"PositionTicks": 18_000_000_000, "IsPaused": false, "PlayMethod": "Transcode", "MediaSourceId": "ms1"},
				"TranscodingInfo": map[string]any{
					"Container": "ts", "VideoCodec": "h264", "AudioCodec": "aac", "IsVideoDirect": false, "IsAudioDirect": true,
					"Bitrate": 8_000_000, "Width": 1920, "Height": 1080, "Framerate": 23.976, "AudioChannels": 2, "CompletionPercentage": 41.26,
					"TranscodeReasons": []string{"ContainerBitrateExceedsLimit"},
					"VideoDecoder":     "hevc_cuvid", "VideoEncoder": "h264_nvenc", "VideoDecoderIsHardware": true, "VideoEncoderIsHardware": true, "VideoEncoderHwAccel": "nvenc",
				},
			},
			{
				"Id": "s2", "UserName": "Plugh", "Client": "Zzyzx Phone", "DeviceName": "Phone", "LastActivityDate": "2026-01-05T08:00:00.0000000Z",
				"NowPlayingItem": map[string]any{"Id": "m2", "Name": "Zzyzx Falling", "Type": "Movie", "RunTimeTicks": 36_000_000_000},
				"PlayState":      map[string]any{"PositionTicks": 0, "IsPaused": true, "PlayMethod": "DirectPlay"},
			},
			{"Id": "s3", "Client": "Zzyzx Web", "DeviceName": "Browser", "LastActivityDate": "2026-01-01T00:00:00.0000000Z"},
		})
	})
	// the file behind what the first session plays, read again for details;
	// the second's item has gone since
	f.mux.HandleFunc("GET /Items", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Ids") != "m1" {
			writeJSON(t, w, page())

			return
		}
		writeJSON(t, w, page(map[string]any{"Id": "m1", "Name": "Zzyzx Rising", "Type": "Movie", "MediaSources": []map[string]any{{
			"Id": "ms1", "Container": "mkv", "Bitrate": 60_000_000, "Size": 54_000_000_000,
			"MediaStreams": []map[string]any{
				{"Type": "Video", "Codec": "hevc", "Width": 3840, "Height": 2160, "BitRate": 58_000_000, "RealFrameRate": 23.976, "VideoRange": "HDR", "ExtendedVideoType": "Hdr10"},
				{"Type": "Audio", "Codec": "truehd", "Language": "eng", "Channels": 8},
			},
		}}}))
	})
	cs := session(t, f, Options{})

	rows := objects(t, mustCall(t, cs, "session_list", map[string]any{})["sessions"], "sessions")
	if len(rows) != 3 {
		t.Fatalf("%d sessions", len(rows))
	}
	first := rows[0]
	if first["play_method"] != "Transcode" || first["progress"] != 25.0 || first["last_activity"] != "2026-01-05T08:34:54.0000000Z" || first["transcoding"] != "video to h264 1920x1080 on hardware (nvenc), audio copied: ContainerBitrateExceedsLimit" {
		t.Errorf("a transcoding session = %v", first)
	}
	if _, ok := first["details"]; ok {
		t.Error("details came without being asked for")
	}
	if second := rows[1]; second["play_method"] != "DirectPlay" || !boolean(t, second["paused"], "paused") || second["transcoding"] != nil || second["progress"] != 0.0 {
		t.Errorf("a session playing the file as it is = %v", second)
	}
	if idle := rows[2]; idle["play_method"] != nil || idle["now_playing"] != nil || idle["last_activity"] != "2026-01-01T00:00:00.0000000Z" {
		t.Errorf("a session playing nothing = %v", idle)
	}
	if len(f.requests("/Items")) != 0 {
		t.Error("the short answer read an item's files")
	}

	rows = objects(t, mustCall(t, cs, "session_list", map[string]any{"details": true})["sessions"], "sessions")
	details := object(t, rows[0]["details"], "details")
	source := object(t, details["source"], "source")
	transcode := object(t, details["transcode"], "transcode")
	if details["app_version"] != "2.1.0" || details["remote_address"] != "10.9.9.12" {
		t.Errorf("the device = %v", details)
	}
	if source["container"] != "mkv" || source["video_codec"] != "hevc" || number(t, source["width"], "width") != 3840 || number(t, source["height"], "height") != 2160 || len(objects(t, source["audio"], "audio")) != 1 {
		t.Errorf("the file being played = %v", source)
	}
	if transcode["video_codec"] != "h264" || boolean(t, transcode["video_direct"], "video_direct") || !boolean(t, transcode["audio_direct"], "audio_direct") || transcode["completion_percent"] != 41.3 || !boolean(t, transcode["encoder_hardware"], "encoder_hardware") || !boolean(t, transcode["decoder_hardware"], "decoder_hardware") ||
		transcode["video_encoder"] != "h264_nvenc" || transcode["hardware_acceleration"] != "nvenc" || len(texts(transcode["reasons"])) != 1 || number(t, transcode["bitrate"], "bitrate") != 8_000_000 {
		t.Errorf("the transcode = %v", transcode)
	}
	// an item gone since it began to play has no file to read, and that is
	// no failure; nor is a session playing nothing
	if d := object(t, rows[1]["details"], "details"); d["source"] != nil || d["transcode"] != nil {
		t.Errorf("details of a session whose item is gone = %v", rows[1]["details"])
	}
	if d := object(t, rows[2]["details"], "details"); d["source"] != nil {
		t.Errorf("details of a session playing nothing = %v", rows[2]["details"])
	}
}

// What a server is doing to a stream, on one line. Jellyfin names the kind
// of acceleration and says nothing of the decoder or the encoder.
func TestTranscodeSummary(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.jellyfin = true
	f.mux.HandleFunc("GET /Sessions", func(w http.ResponseWriter, _ *http.Request) {
		transcode := func(accel string, videoDirect bool) map[string]any {
			return map[string]any{
				"Id": "s-" + accel, "Client": "Zzyzx", "DeviceName": accel,
				"NowPlayingItem": map[string]any{"Id": "m1", "Name": "Zzyzx Rising", "Type": "Movie", "RunTimeTicks": 0},
				"PlayState":      map[string]any{"PlayMethod": "Transcode"},
				"TranscodingInfo": map[string]any{
					"VideoCodec": "h264", "AudioCodec": "aac", "IsVideoDirect": videoDirect, "IsAudioDirect": false, "Width": 1280, "Height": 720,
					"HardwareAccelerationType": accel, "TranscodeReasons": []string{"VideoCodecNotSupported", "AudioCodecNotSupported"},
				},
			}
		}
		writeJSON(t, w, []map[string]any{transcode("nvenc", false), transcode("none", false), transcode("vaapi", true)})
	})
	rows := objects(t, mustCall(t, session(t, f, Options{}), "session_list", map[string]any{})["sessions"], "sessions")
	for i, want := range []string{
		"video to h264 1280x720 on hardware (nvenc), audio to aac: VideoCodecNotSupported, AudioCodecNotSupported",
		"video to h264 1280x720 in software, audio to aac: VideoCodecNotSupported, AudioCodecNotSupported",
		"video copied, audio to aac: VideoCodecNotSupported, AudioCodecNotSupported",
	} {
		if got := rows[i]["transcoding"]; got != want {
			t.Errorf("session %d: transcoding = %q\nwant %q", i, got, want)
		}
		// a length of nothing gives no progress to state
		if rows[i]["progress"] != nil {
			t.Errorf("session %d: progress through an item of no length = %v", i, rows[i]["progress"])
		}
	}
}

// server_info says whether the server waits for a restart or has an update
// out, and reads from the top of its log what its API does not give: the
// processors it counted, when it started, where it keeps things. A server
// whose logs begin with no start says that, rather than nothing.
func TestServerInfoReadsTheStartOfTheLog(t *testing.T) {
	t.Parallel()

	const start = "2026-01-05 08:00:00.201 Info Main: Emby Server 4.10.1.0\n" +
		"\tOperating system: Linux version 6.1 (...)\n" +
		"\tOS/Process: x64/x64\n" +
		"\tProcessor count: 16\n" +
		"\tData path: /config\n" +
		"2026-01-05 08:00:00.202 Info Main: Logs path: /config/logs\n"
	server := func(logs map[string]string) *fakeServer {
		f := newFakeServer(t)
		f.mux.HandleFunc("GET /System/Info", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, map[string]any{"ServerName": "Zzyzx", "Version": "4.10.1.0", "OperatingSystem": "Linux", "HasPendingRestart": true, "HasUpdateAvailable": false, "CanSelfRestart": true})
		})
		f.mux.HandleFunc("GET /System/Logs/Query", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, page(
				map[string]any{"Name": "embyserver.txt", "DateModified": "2026-01-06T09:00:00Z"},
				map[string]any{"Name": "embyserver-63900000000.txt", "DateModified": "2026-01-05T23:59:59Z"},
				map[string]any{"Name": "ffmpeg-transcode-0badc0de.txt", "DateModified": "2026-01-06T10:00:00Z"},
			))
		})
		f.mux.HandleFunc("GET /System/Logs/{name}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, logs[r.PathValue("name")])
		})

		return f
	}

	// today's log began at midnight; yesterday's began at the start
	f := server(map[string]string{"embyserver.txt": "2026-01-06 00:00:00.000 Info App: a new day\n", "embyserver-63900000000.txt": start})
	out := mustCall(t, session(t, f, Options{}), "server_info", map[string]any{})
	paths := object(t, out["paths"], "paths")
	if !boolean(t, out["pending_restart"], "pending_restart") || boolean(t, out["update_available"], "update_available") || !boolean(t, out["can_self_restart"], "can_self_restart") || number(t, out["processors"], "processors") != 16 || out["started"] != "2026-01-05 08:00:00.201" || out["architecture"] != "x64" || paths["data"] != "/config" || paths["logs"] != "/config/logs" || out["note"] != nil {
		t.Errorf("server_info = %v", out)
	}
	if len(f.requests("/System/Logs/ffmpeg-transcode-0badc0de.txt")) != 0 {
		t.Error("a transcode's log was read for the server's start")
	}

	f = server(map[string]string{"embyserver.txt": "2026-01-06 00:00:00.000 Info App: a new day\n"})
	out = mustCall(t, session(t, f, Options{}), "server_info", map[string]any{})
	if out["processors"] != nil || out["started"] != nil || !strings.Contains(text(out["note"]), "none of the server's recent logs begins with a start") || out["server_version"] != "4.10.1.0" {
		t.Errorf("server_info with no start in the logs = %v", out)
	}
}
