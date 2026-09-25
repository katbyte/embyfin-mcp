#!/usr/bin/env bash
#
# Bring up a throwaway Emby or Jellyfin in Docker, lay out a known catalogue of
# tiny videos for it, complete its setup wizard, mint an API key, and print the
# environment the live test suites need.
#
#   eval "$(EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh up)"   # start, export EMBYFIN_*
#   EMBYFIN_TEST_BACKEND=jellyfin scripts/testenv.sh down            # stop and remove everything
#   scripts/testenv.sh fixtures                                      # write the media tree only
#
# This script only does what embyfin-mcp cannot: run the container, get through
# the first-run wizard, create the users, mint the API key, and lay the media
# out on disk. The libraries, the scans and the metadata edits are the test
# suites' job, through library_create, library_scan and item_edit, so those
# tools are exercised rather than bypassed.
#
# The container is pointed at the record/replay proxy the tests run (see
# lib/providerproxy): the media server, not embyfin-mcp, is what calls TMDB,
# TheTVDB, OMDb and the image CDNs, so intercepting those calls has to happen
# at its edge. Both servers are .NET apps, which honour HTTPS_PROXY and, on
# Linux, trust whatever SSL_CERT_FILE names - so the proxy's certificate
# authority is minted here (openssl) and mounted in, and the tests load the
# same files to sign with.

set -euo pipefail

BACKEND="${EMBYFIN_TEST_BACKEND:-}"
case "$BACKEND" in
  emby)
    IMAGE="${EMBYFIN_TEST_IMAGE:-emby/embyserver:latest}"
    PORT="${EMBYFIN_TEST_PORT:-18097}"
    ;;
  jellyfin)
    IMAGE="${EMBYFIN_TEST_IMAGE:-jellyfin/jellyfin:latest}"
    PORT="${EMBYFIN_TEST_PORT:-18096}"
    ;;
  "")
    if [ "${1:-up}" != "fixtures" ]; then
      echo "EMBYFIN_TEST_BACKEND must be emby or jellyfin" >&2
      exit 1
    fi
    ;;
  *)
    echo "EMBYFIN_TEST_BACKEND=$BACKEND: want emby or jellyfin" >&2
    exit 1
    ;;
esac

NAME="${EMBYFIN_TEST_CONTAINER:-embyfin-mcp-test-${BACKEND}}"
# the port the tests' provider proxy listens on, reached from inside the
# container via host.docker.internal
PROXY_PORT="${EMBYFIN_TEST_PROXY_PORT:-18080}"
# not TMPDIR: on macOS that is /var/folders/..., which Docker Desktop does not
# share by default, and the bind mounts silently come up empty
DATA="${EMBYFIN_TEST_DATA:-${HOME}/.cache/embyfin-mcp/testenv/${BACKEND:-fixtures}}"
# the proxy's certificate authority, shared by every container so one set of
# cassettes serves them all
PROXY_CA="${EMBYFIN_TEST_PROXY_CA:-${HOME}/.cache/embyfin-mcp/testenv/proxy}"
ADMIN="root"
PASSWORD="embyfin-mcp-test"
# a second, ordinary account, so the user tools have someone other than the
# key's own user to look at
USER2="alice"
# no backend means "fixtures", which needs no server: leave the port empty
URL="http://127.0.0.1:${PORT:-}"

# proxy_host is the address the container reaches the test proxy on. Docker
# Desktop provides host.docker.internal; on Linux docker maps it with
# --add-host, and we hand the server the bridge gateway's address instead, so
# a runtime that prefers an IPv6 answer cannot pick a route the host does not
# listen on - which is how every provider lookup came to time out in CI while
# busybox nc, which takes the first address, reached the proxy happily.
proxy_host() {
  if [ "$(uname -s)" = "Linux" ]; then
    docker network inspect bridge -f '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null && return 0
  fi
  echo "host.docker.internal"
}

log() { echo "==> $*" >&2; }

# api METHOD PATH [BODY] [TOKEN] - curl against the test server, failing loudly.
# The token goes in the header both servers read.
api() {
  local method=$1 path=$2 body=${3:-} token=${4:-}
  local args=(-fsS -X "$method" "${URL}${path}" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "X-Emby-Token: ${token}" -H "Authorization: MediaBrowser Token=\"${token}\", Client=\"embyfin-mcp-testenv\", Device=\"testenv\", DeviceId=\"testenv\", Version=\"0\"")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}

# ---------------------------------------------------------------------------
# The catalogue. Real titles with their real provider ids, so the servers'
# own metadata fetchers (recorded through the proxy) and the audits have
# something true to compare against. Change a line here and the suites'
# fixture tables (acceptance/harness_test.go, integration/harness_test.go)
# must agree.
#
# folder|title|year|tmdb|imdb|runtime minutes|genre|director|plot
MOVIES='Alien (1979)|Alien|1979|348|tt0078748|117|Horror|Ridley Scott|After a space merchant vessel receives an unknown transmission as a distress call, one of the crew is attacked by a mysterious life form.
Aliens (1986)|Aliens|1986|679|tt0090605|137|Action|James Cameron|Ripley returns to the planet where her crew encountered the hostile Alien organism, this time with a unit of colonial marines.
Blade Runner (1982)|Blade Runner|1982|78|tt0083658|117|Science Fiction|Ridley Scott|In the smog-choked dystopian Los Angeles of 2019, blade runner Rick Deckard is called out of retirement to terminate a quartet of replicants.
Dune (2021)|Dune|2021|438631|tt1160419|155|Science Fiction|Denis Villeneuve|Paul Atreides leads nomadic tribes in a revolt against the evil Harkonnens.
Dune Part Two (2024)|Dune: Part Two|2024|693134|tt15239678|167|Science Fiction|Denis Villeneuve|Paul Atreides unites with Chani and the Fremen while seeking revenge against the conspirators who destroyed his family.
Princess Mononoke (1997)|Princess Mononoke|1997|128|tt0119698|134|Animation|Hayao Miyazaki|Ashitaka, a prince cursed by a demon boar god, travels west in search of a cure and finds himself caught in a war between the gods of the forest and a mining town that is destroying it.
Arrival (2016)|Arrival|2016|329865|tt2543164|116|Drama|Denis Villeneuve|Taking place after alien crafts land around the world, an expert linguist is recruited by the military to determine whether they come in peace.
The Thirteenth Floor (1999)|The Thirteenth Floor|1999|1090|tt0139809|100|Science Fiction|Josef Rusnak|When his mentor is murdered, a computer scientist enters the simulation of 1937 Los Angeles they built together to find the killer, and begins to doubt that his own world is real.
Limitless (2011)|Limitless|2011|51876|tt1219289|106|Thriller|Neil Burger|The life of an unsuccessful writer is transformed by a top-secret '\''smart drug'\'' that allows him to use 100% of his brain and become a perfect version of himself. His enhanced abilities soon attract shadowy forces that threaten his new life.'

# folder|title|year|tmdb|tvdb|imdb|genre|plot
SHOWS='Severance|Severance|2022|95396|371980|tt11280740|Drama|Mark leads a team of office workers whose memories have been surgically divided between their work and personal lives.
Breaking Bad|Breaking Bad|2008|1396|81189|tt0903747|Drama|A chemistry teacher diagnosed with cancer turns to manufacturing methamphetamine to secure his family'\''s future.
The Expanse|The Expanse|2015|63639|280619|tt3230854|Science Fiction|A police detective in the asteroid belt, the first officer of an interplanetary ice freighter and an earth-bound UN executive slowly discover a vast conspiracy.
Limitless|Limitless|2015|62687|295743|tt4422836|Drama|Brian Finch'\''s life takes an extraordinary turn when he uses NZT-48, a neuroenhancing drug whose mystery and chaos lead him to working for the FBI and a senator who is not what he seems.'

# show folder|season|episode|title - the episode files on disk. Severance
# season one has nine episodes and we hold two, so with the provider on the
# server lists the rest as missing; Breaking Bad holds three of seven. The
# Expanse holds the first of TMDB's specials, in Season 00.
#
# The clean library carries two defects of its own, on purpose, for the
# audits that compare an episode's title with its season's and its file's:
# Breaking Bad's third episode carries the second's title (the title
# audit_duplicate_episodes finds twice in a season), and The Expanse's
# second episode is in a file named after the first (a fifth field names the
# file after something other than the episode, which audit_file_path's title
# check catches).
#
# Limitless is the series of 2015 beside the film of 2011 in the clean
# movies: one name for a film and a show, which a lookup by name must keep
# apart.
EPISODES='Severance|1|1|Good News About Hell
Severance|1|2|Half Loop
Severance|2|1|Hello, Ms. Cobel
Severance|2|2|Goodbye, Mrs. Selvig
Breaking Bad|1|1|Pilot
Breaking Bad|1|2|Cat'\''s in the Bag...
Breaking Bad|1|3|Cat'\''s in the Bag...
The Expanse|0|1|Inside The Expanse: Episode 1
The Expanse|1|1|Dulcinea
The Expanse|1|2|The Big Empty|Dulcinea
Limitless|1|1|Pilot'

# The music library. Four artists with their real MusicBrainz ids and a
# subset of each album's real tracklist, so the tags the scanner reads are
# true; the files themselves are one-second sine tones. The library is
# created with the fetchers off like the messy ones, so the tags are all the
# servers know, and it carries two defects of its own: Thundercolor has no
# cover art (audit_missing_poster with types=MusicAlbum finds it) and
# SirensCeol is tagged "Electronica" where the other electronic acts say
# "Electronic" (audit_spelling finds the pair). Thundercolor is the rip with
# no art at all - no cover.jpg beside the tracks and no picture embedded in
# them - which is what a poster audit pointed at music looks for.
#
# The tags carry the defects a tagger leaves behind, one album's worth each,
# for the servers' readings of them to be pinned:
#   Polygon                    tagged with an album artist of Various Artists over Battle Tapes' tracks
#   Thundercolor               no MusicBrainz ids at all (a rip nothing was looked up for), and its
#                              third track numbered 2 and its fourth not numbered: a number twice and
#                              one missing
#   The Dark Side of the Moon  two discs, a folder each ("Disc 1", "Disc 2"), numbered from 1 on each
#   Wish You Were Here         one track tagged with the album misspelt (Wish You Where Here), and one
#                              with the artist spelled another way (The Pink Floyd): an album split by
#                              one track's tag, and an artist written two ways
#
# artist|musicbrainz artist id|genre
ARTISTS='Battle Tapes|82178603-e97b-4d60-b521-82582545a0a8|Electronic
Coyote Kisses|09c08ae4-0b3e-4e06-9892-c6a1ec3c9d6c|Electronic
Pink Floyd|83d91898-7763-47d7-b03b-b92132375c47|Progressive Rock
SirensCeol|621dac65-5eac-4ad3-a630-05f282bbe4e2|Electronica'

# artist|album|year|musicbrainz release id|cover or nocover|album artist when
# it is not the artist. An album with no release id was never looked up, so
# its tracks carry neither id.
ALBUMS='Battle Tapes|Polygon|2015|0e715a3a-8461-4620-8843-6c4324c64d49|cover|Various Artists
Coyote Kisses|Thundercolor|2013||nocover
Pink Floyd|The Dark Side of the Moon|1973|b84ee12a-09ef-421b-82de-0441a926375b|cover
Pink Floyd|Wish You Were Here|1975|f4a8aa35-da90-33d8-9307-c630d38a2bed|cover
SirensCeol|Afterworld|2016|00cc6656-b7c3-4c33-8df3-5909e46979b2|cover'

# artist|album|[disc-]track number|title|what one of its tags says instead -
# the first four of each album. The position is where the file sits; a fifth
# field (track=, artist= or album=) is a tag that disagrees with it, and
# track= with nothing after it is no track number at all.
TRACKS='Battle Tapes|Polygon|1|Belgrade
Battle Tapes|Polygon|2|Valkyrie
Battle Tapes|Polygon|3|Solid Gold
Battle Tapes|Polygon|4|Private Dancer
Coyote Kisses|Thundercolor|1|Diving At Night
Coyote Kisses|Thundercolor|2|Stay With You
Coyote Kisses|Thundercolor|3|This Is How You Know|track=2
Coyote Kisses|Thundercolor|4|Changing Guard|track=
Pink Floyd|The Dark Side of the Moon|1-1|Speak to Me
Pink Floyd|The Dark Side of the Moon|1-2|Breathe
Pink Floyd|The Dark Side of the Moon|2-1|On the Run
Pink Floyd|The Dark Side of the Moon|2-2|Time
Pink Floyd|Wish You Were Here|1|Shine On You Crazy Diamond, Parts I-V
Pink Floyd|Wish You Were Here|2|Welcome to the Machine|album=Wish You Where Here
Pink Floyd|Wish You Were Here|3|Have a Cigar|artist=The Pink Floyd
Pink Floyd|Wish You Were Here|4|Wish You Were Here
SirensCeol|Afterworld|1|Welcome to the Afterworld
SirensCeol|Afterworld|2|The Future We Built
SirensCeol|Afterworld|3|Afterworld
SirensCeol|Afterworld|4|A Grand Illusion'

# The messy libraries: the defects the audits exist to find, laid out the way
# a real collection accumulates them. Their files are 360p rips (the clean
# libraries' are 720p), so audit_quality has a worklist there and nothing to
# say about the clean ones. Their libraries are created with the
# metadata fetchers off, so what the nfo files say is what the server knows,
# and the acceptance harness sets the rest with item_edit.
#
# messy-movies:
#   Princess Mononoke (1997)       no nfo at all: unmatched, no overview, no poster; MPEG-4 part 2
#                                  video, the legacy codec audit_quality looks for; and two audio
#                                  tracks, Japanese then English, so an English one second in line
#                                  is still English audio
#   Arrival (2016)                 nfo with ids but no plot, and no poster
#   Dune (2021)                    folder says 2021, nfo says 1984 (David Lynch's film, the other
#                                  Dune): year mismatch
#   Alien (1979) + Directors Cut   two folders, one tmdb id: duplicates (and a third copy sits in the clean library)
#   Blade Runner (1982)            two files in one folder, 1080p at 24 frames a second and an AI upscale
#                                  of it: 2160p at 60, HEVC 10-bit with HDR10's colour tags. Multiple
#                                  versions on Jellyfin, which folds them into one entry; two films to a
#                                  sweep on Emby, which merges them only in a user's view of the item (so
#                                  a duplicate there). Neither file is below 720p, so audit_quality
#                                  leaves the film alone
#   Interstellar (2014)            a trailer beside the film (-trailer), and Trailers and Extras
#                                  folders: what goes with a film and is not another copy of it, which
#                                  neither server lists as a film or a version. Its nfo says 169
#                                  minutes, which neither server keeps over the file's second
#   Coyote vs. Acme (2026)         a DVD's VTS_01_1.VOB left loose in a film's folder: a flattened disc,
#                                  and no nfo, so unmatched with no overview or poster
#   Cube (1997)                    a Blu-ray kept whole (BDMV/STREAM): both servers hold it as one film
#                                  at its folder, which the disc audit must leave alone, and never probe
#                                  it, so audit_quality has nothing to judge it by
#   Moon (2009)                    a DVD kept whole (VIDEO_TS with its IFO, BUP and VOB files, and its
#                                  nfo as VIDEO_TS.nfo): one film at its folder, like Cube, which the
#                                  disc audit must leave alone. Jellyfin probes the disc, Emby does not
#   Memento (2000)                 nfo carries Breaking Bad's IMDb id and no TMDB one: a series' id on a film
#   Star Wars Episode IV - A New Hope Despecialized Edition (1977)
#                                  the fan restoration of Star Wars: nfo carries a TMDB id TMDB has no
#                                  film for, and the genre spelled Science-Fiction where the rest say
#                                  Science Fiction; its one audio track is German, tagged ger, which
#                                  audit_language must read as deu
# messy-shows:
#   Severance                      season one, three episodes, the third five seconds long, the second
#                                  a DVD rip: 720x480 stated as 16:9 (anamorphic); and an Extras folder
#                                  in season one: a featurette, which Emby holds as an episode with no
#                                  number and Jellyfin as the season's extra
#   Star Trek The Next Generation  no tvshow.nfo: unmatched; episodes 1 and 3, no 2
#   Star Trek Deep Space Nine (1993)
#                                  no ids; one episode of season 1 and one of season 3, no season 2;
#                                  the season 3 file an mkv whose duration claims twelve hours for a
#                                  second of video: broken metadata
#   Andor (2022)                   no ids; files whose names disagree with their nfo: S01E04 held as
#                                  episode 5, another series' file (Breaking Bad S01E06), S02E08 held in
#                                  season 1, a two-episode file (S01E02E03) whose nfo ends the run at 2,
#                                  and one named without a marker (07 - Announcement); genre Science Fiction
#   A Knight of the Seven Kingdoms (2026) twice
#                                  two folders a space and a letter's case apart: one show held twice
#   hack Liminality (2002)         .hack//Liminality, known by an AniDB id alone (222): an OVA the anime
#                                  list says TVDB and TMDB fold into .hack//SIGN's specials; genre
#                                  Science-Fiction; two episodes of three minutes and a third of one
#                                  second: a runtime outlier
#   The Wire + The Wire (2002)     one show split by a folder rename: both tvshow.nfo files carry its
#                                  ids, the first two episodes are in one folder and the second and
#                                  third in the other, the second held twice - a 720p copy in the
#                                  renamed folder beside the 360p one. The folders differ by the year,
#                                  so the folder audit keeps them apart and the id audit joins them
#   Asterix & Obelix - The Big Fight (2025)
#                                  the 2025 series, its tvshow.nfo carrying the ids of the 1989 film
#                                  (TMDB 11625, which as a series' number is another show entirely,
#                                  and the film's IMDb id): a film's ids on a show
#   Red Dwarf                      ids, and files for season 3 alone: everything before it is missing,
#                                  which only a provider's run can say

# wipe_data removes the data directory. The container writes its config,
# metadata and cache as its own user, and on Linux those land root-owned (or
# uid 2 for Emby) in the bind mount where the calling user cannot delete
# them (Docker Desktop on macOS remaps them, which is why this only bites in
# CI). A throwaway root container can always remove them.
wipe_data() {
  [ -d "${DATA}" ] || return 0
  rm -rf "${DATA}" 2>/dev/null && return 0

  log "removing container-owned files"
  docker run --rm -v "${DATA}:/data" alpine:3 sh -c 'rm -rf /data/..?* /data/.[!.]* /data/*' >/dev/null 2>&1 || true
  rm -rf "${DATA}" 2>/dev/null || true
}

# field TABLE KEYCOL KEY COL - the COLth field of the first row of a
# pipe-delimited table whose KEYCOLth field is KEY.
field() {
  printf '%s\n' "$1" | awk -F'|' -v k="$3" -v kc="$2" -v c="$4" '$kc == k {print $c; exit}'
}

# video PATH [SECONDS] [SIZE] [CODEC] [LANGUAGE] [SECOND LANGUAGE] [RATE] - a
# short file of that length, 720p h264 at five frames a second unless the
# rest say otherwise, so the scanner has a real container to probe and a
# runtime to report. At five frames a second a 720p second is tens of
# kilobytes and a 2160p one under a megabyte.
video() {
  mkdir -p "$(dirname "$1")"
  # -nostdin matters: without it ffmpeg reads the while-read loop's stdin
  # looking for interactive keys and swallows a character of the next line,
  # which silently truncates the titles that follow.
  #
  # a fifth argument tags the audio track's language; without one the track
  # is untagged, which is what most of the fixtures are, so audit_language
  # sees tagged, untagged and a language that is not there. A sixth adds a
  # second track in that language after the first. Conditional expansion
  # rather than an array: macOS's bash 3.2 under set -u refuses an empty one.
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "testsrc2=s=${3:-1280x720}:r=${7:-5}" -f lavfi -i "anullsrc=r=22050:cl=mono" \
    ${6:+-f lavfi -i anullsrc=r=22050:cl=mono -map 0:v -map 1:a -map 2:a} \
    -t "${2:-1}" -c:v "${4:-libx264}" -pix_fmt yuv420p -c:a aac \
    ${5:+-metadata:s:a:0} ${5:+"language=$5"} ${6:+-metadata:s:a:1} ${6:+"language=$6"} -shortest "$1"
}

# upscale PATH - a second of what an AI upscaler sells as 4K HDR: 2160p at 60
# frames a second, HEVC 10-bit, with HDR10's colour tags (PQ transfer, BT.2020
# primaries). Nothing about the picture is HDR; the tags are the claim, and 60
# frames is the interpolation no film master has. setparams puts the tags on
# the frames, which is where the encoder takes them from.
upscale() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "testsrc2=s=3840x2160:r=60" -f lavfi -i "anullsrc=r=22050:cl=mono" -t 1 \
    -vf "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc" \
    -c:v libx265 -preset ultrafast -crf 40 -x265-params log-level=error -pix_fmt yuv420p10le -tag:v hvc1 \
    -c:a aac -shortest "$1"
}

# anamorphic PATH - a second of a DVD rip as it comes off the disc: 720x480,
# its pixels stretched to 16:9 by the ratio the stream states (32:27), which
# is the only thing telling it from a 3:2 or a 4:3 picture of the same size.
anamorphic() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "testsrc2=s=720x480:r=5" -f lavfi -i "anullsrc=r=22050:cl=mono" -t 1 \
    -vf setsar=32/27 -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest "$1"
}

# claims_hours PATH - a second of video in an mkv whose duration says twelve
# hours: the last frame is stamped at the twelfth hour and the muxer writes
# the duration from it, the way a broken remux leaves one.
claims_hours() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -t 1 -i "testsrc2=s=640x360:r=5" -f lavfi -t 1 -i "anullsrc=r=22050:cl=mono" \
    -vf "setpts='if(eq(N,4),43200/TB,PTS)'" -fps_mode passthrough \
    -c:v libx264 -pix_fmt yuv420p -c:a aac "$1"
}

# full_length PATH MINUTES - a film that runs its real length at next to no
# cost: a still 720p frame once a second, which the encoder repeats for a few
# bytes, and silent audio, so a runtime check has a film that matches the
# provider's and every other fact reads as the clean fixtures' do. The video
# is padded to a steady 2 kbps, so a bitrate floor of a kilobit a second
# still has nothing to say about it.
full_length() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "color=c=gray:s=1280x720:r=1" -f lavfi -i "anullsrc=r=8000:cl=mono" -t "$(($2 * 60))" \
    -c:v libx264 -g 100000 -b:v 2k -minrate 2k -maxrate 2k -bufsize 4k -x264-params nal-hrd=cbr \
    -pix_fmt yuv420p -c:a aac -b:a 8k -shortest "$1"
}

# ifo PATH MAGIC - a DVD's navigation file, as far as a scanner reads one:
# the identifier that says which kind it is (DVDVIDEO-VMG for the disc,
# DVDVIDEO-VTS for a title set) in a 2 KB sector. A .BUP is the same file
# again, which is what a disc carries it as.
ifo() {
  mkdir -p "$(dirname "$1")"
  { printf '%s' "$2"; head -c $((2048 - ${#2})) /dev/zero; } >"$1"
}

# subtitle PATH - a one-line SubRip file. The servers read its language from
# the name: "Film (1999).eng.srt" beside "Film (1999).mp4" is English.
subtitle() {
  mkdir -p "$(dirname "$1")"
  printf '1\n00:00:00,000 --> 00:00:00,900\nA line.\n' >"$1"
}

# poster PATH - a 2:3 jpeg with a test pattern, so the scanner picks it up
# as the item's primary image and the poster audit has something to leave alone.
poster() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y -f lavfi -i "testsrc2=s=200x300" -frames:v 1 "$1"
}

# cover PATH - square album art, so an album has a primary image the way a
# ripped library does and the poster audit has one to leave alone.
cover() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y -f lavfi -i "testsrc2=s=300x300" -frames:v 1 "$1"
}

# audio FILE TITLE TRACK OF ARTIST ALBUM YEAR GENRE MBARTIST MBALBUM [COVER]
# [ALBUM ARTIST] [DISC] - a one-second tone tagged as the track it stands
# for, with COVER embedded as its album art the way a ripper leaves it. The
# servers read a song from its tags, not its name, so this is what they have
# to go on with the fetchers off; the MusicBrainz ids go in as the TXXX frames
# every ripper writes. An empty TRACK or id leaves that tag out, the album
# artist is the artist unless given, and DISC ("2/2") tags which disc it is on.
audio() {
  local file=$1 title=$2 track=$3 of=$4 artist=$5 album=$6 year=$7 genre=$8 mbartist=$9 mbalbum=${10} art=${11:-}
  local album_artist=${12:-$5} disc=${13:-}
  mkdir -p "$(dirname "$file")"
  local art_in=() art_map=()
  if [ -n "$art" ]; then
    art_in=(-i "$art")
    art_map=(-map 1:v -c:v mjpeg -disposition:v attached_pic
      -metadata:s:v "title=Album cover" -metadata:s:v "comment=Cover (front)")
  fi
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "sine=frequency=$((220 + ${track:-0} * 55)):duration=1:sample_rate=44100" \
    "${art_in[@]}" -map 0:a "${art_map[@]}" \
    -c:a libmp3lame -b:a 64k -id3v2_version 3 \
    -metadata "title=${title}" \
    ${track:+-metadata} ${track:+"track=${track}/${of}"} \
    ${disc:+-metadata} ${disc:+"disc=${disc}"} \
    -metadata "artist=${artist}" \
    -metadata "album_artist=${album_artist}" \
    -metadata "album=${album}" \
    -metadata "date=${year}" \
    -metadata "genre=${genre}" \
    ${mbartist:+-metadata} ${mbartist:+"MusicBrainz Artist Id=${mbartist}"} \
    ${mbalbum:+-metadata} ${mbalbum:+"MusicBrainz Album Id=${mbalbum}"} \
    "$file"
}

# lyrics PATH - a timed .lrc sidecar, the shape Jellyfin's lyric reader
# expects. The words are the fixture's own, not the song's.
lyrics() {
  mkdir -p "$(dirname "$1")"
  {
    echo "[ar:SirensCeol]"
    echo "[ti:The Future We Built]"
    echo "[00:00.00] one second of a sine wave"
    echo "[00:00.50] standing in for a song"
  } > "$1"
}

# xml_escape - the few characters that matter in element text.
xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

# movie_nfo DIR TITLE YEAR TMDB IMDB RUNTIME GENRE DIRECTOR PLOT - a Kodi-style
# movie.nfo, which both servers read when the Nfo reader is on (it is by
# default). An empty PLOT leaves the element out.
movie_nfo() {
  local dir=$1 title=$2 year=$3 tmdb=$4 imdb=$5 runtime=$6 genre=$7 director=$8 plot=$9
  mkdir -p "$dir"
  {
    echo '<?xml version="1.0" encoding="utf-8"?>'
    echo '<movie>'
    echo "  <title>$(xml_escape "$title")</title>"
    echo "  <year>${year}</year>"
    [ -n "$plot" ] && echo "  <plot>$(xml_escape "$plot")</plot>"
    [ -n "$runtime" ] && echo "  <runtime>${runtime}</runtime>"
    [ -n "$genre" ] && echo "  <genre>$(xml_escape "$genre")</genre>"
    [ -n "$director" ] && echo "  <director>$(xml_escape "$director")</director>"
    [ -n "$tmdb" ] && echo "  <tmdbid>${tmdb}</tmdbid>" && echo "  <uniqueid type=\"tmdb\" default=\"true\">${tmdb}</uniqueid>"
    [ -n "$imdb" ] && echo "  <imdbid>${imdb}</imdbid>" && echo "  <uniqueid type=\"imdb\">${imdb}</uniqueid>"
    echo '</movie>'
  } > "${dir}/movie.nfo"
}

# show_nfo DIR TITLE YEAR TMDB TVDB IMDB GENRE PLOT - a tvshow.nfo.
show_nfo() {
  local dir=$1 title=$2 year=$3 tmdb=$4 tvdb=$5 imdb=$6 genre=$7 plot=$8
  mkdir -p "$dir"
  {
    echo '<?xml version="1.0" encoding="utf-8"?>'
    echo '<tvshow>'
    echo "  <title>$(xml_escape "$title")</title>"
    echo "  <year>${year}</year>"
    [ -n "$plot" ] && echo "  <plot>$(xml_escape "$plot")</plot>"
    [ -n "$genre" ] && echo "  <genre>$(xml_escape "$genre")</genre>"
    [ -n "$tmdb" ] && echo "  <uniqueid type=\"tmdb\" default=\"true\">${tmdb}</uniqueid>"
    [ -n "$tvdb" ] && echo "  <tvdbid>${tvdb}</tvdbid>" && echo "  <uniqueid type=\"tvdb\">${tvdb}</uniqueid>"
    [ -n "$imdb" ] && echo "  <uniqueid type=\"imdb\">${imdb}</uniqueid>"
    echo '</tvshow>'
  } > "${dir}/tvshow.nfo"
}

# episode FILE_BASE SEASON EPISODE TITLE [SECONDS] [SIZE] [FILETITLE] [END] - one
# episode with its nfo. The season and episode are the nfo's: a FILE_BASE
# numbered otherwise is a file named for one episode that holds another.
episode() {
  local base=$1 season=$2 ep=$3 title=$4 secs=${5:-1} size=${6:-1280x720} filetitle=${7:-} end=${8:-}
  # a seventh argument names the FILE after something other than the episode,
  # which is what a file written from another series looks like: the nfo keeps
  # the real title, and audit_file_path's title check has the two to compare
  [ -z "$filetitle" ] || base="${base} - ${filetitle}"
  video "${base}.mp4" "$secs" "$size"
  # an eighth ends the episode run the nfo claims, which Jellyfin reads over a
  # run in the file name and Emby does not
  {
    echo '<?xml version="1.0" encoding="utf-8"?>'
    echo '<episodedetails>'
    echo "  <title>$(xml_escape "$title")</title>"
    echo "  <season>${season}</season>"
    echo "  <episode>${ep}</episode>"
    [ -z "$end" ] || echo "  <episodenumberend>${end}</episodenumberend>"
    echo "  <plot>Episode ${ep} of season ${season}: $(xml_escape "$title").</plot>"
    echo '</episodedetails>'
  } > "${base}.nfo"
}

# vob PATH - one second of a DVD's video: MPEG-2 in a program stream at the
# DVD's own size, the file a disc's titleset parts are.
vob() {
  mkdir -p "$(dirname "$1")"
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "testsrc2=s=720x480:r=25" -f lavfi -i "anullsrc=r=48000:cl=stereo" \
    -t 1 -c:v mpeg2video -c:a mp2 -f vob "$1"
}

fixtures() {
  log "generating video fixtures under ${DATA}"
  wipe_data

  # the clean movies: one folder each, nfo with the real ids, a poster
  while IFS='|' read -r folder title year tmdb imdb runtime genre director plot; do
    [ -n "$folder" ] || continue
    dir="${DATA}/media/movies/${folder}"
    video "${dir}/${folder}.mp4"
    movie_nfo "$dir" "$title" "$year" "$tmdb" "$imdb" "$runtime" "$genre" "$director" "$plot"
    poster "${dir}/poster.jpg"
  done <<<"$MOVIES"
  # one film carries an English subtitle, for audit_language to find
  subtitle "${DATA}/media/movies/The Thirteenth Floor (1999)/The Thirteenth Floor (1999).eng.srt"
  # and one runs its real length, so audit_provider's runtime check has a
  # film it must leave alone among the rest, whose files run a second, and a
  # play can stop part way through it
  full_length "${DATA}/media/movies/Limitless (2011)/Limitless (2011).mp4" 106

  # the clean shows: "<show>/Season NN/<show> SNNENN.mp4" with an nfo beside each
  while IFS='|' read -r folder title year tmdb tvdb imdb genre plot; do
    [ -n "$folder" ] || continue
    dir="${DATA}/media/shows/${folder}"
    show_nfo "$dir" "$title" "$year" "$tmdb" "$tvdb" "$imdb" "$genre" "$plot"
    poster "${dir}/poster.jpg"
  done <<<"$SHOWS"
  while IFS='|' read -r show season ep title filetitle; do
    [ -n "$show" ] || continue
    episode "$(printf '%s/media/shows/%s/Season %02d/%s S%02dE%02d' "$DATA" "$show" "$season" "$show" "$season" "$ep")" "$season" "$ep" "$title" 1 1280x720 "$filetitle"
  done <<<"$EPISODES"

  # two Blu-ray streams, outside every library: the disc audit's live test
  # copies them into a film's folder to make the shape it looks for, and takes
  # them away again. Left in a library they would be two more films for every
  # test that counts one.
  video "${DATA}/media/disc-src/00000.m2ts" 1 320x180
  video "${DATA}/media/disc-src/00001.m2ts" 1 320x180

  # a special long enough to be one, outside every library: the anime audit
  # takes anything under three minutes among a show's specials for an
  # opening or a trailer, and its live test copies this into a show it stages
  video "${DATA}/media/anime-src/special.mp4" 200 160x90

  # frames of other shapes than 16:9, outside every library, which the
  # quality audit's live test copies over a film to read its resolution
  # class by: two scope encodes and a 4:3 one it must leave alone, and a
  # scope DVD it must not
  for size in 1920x800 1280x536 960x720 720x304; do
    video "${DATA}/media/frames-src/${size}.mp4" 1 "$size"
  done

  # the messy movies
  m="${DATA}/media/messy-movies"
  video "${m}/Princess Mononoke (1997)/Princess Mononoke (1997).mp4" 1 640x360 mpeg4 jpn eng
  video "${m}/Arrival (2016)/Arrival (2016).mp4" 1 640x360
  movie_nfo "${m}/Arrival (2016)" "Arrival" 2016 329865 tt2543164 116 Drama "Denis Villeneuve" ""
  video "${m}/Dune (2021)/Dune (2021).mp4" 1 640x360
  movie_nfo "${m}/Dune (2021)" "Dune" 1984 841 tt0087182 137 "Science Fiction" "David Lynch" "In the year 10191, the heir of House Atreides is drawn into a war for the desert planet Arrakis, the only source of the spice that makes travel between the stars possible."
  poster "${m}/Dune (2021)/poster.jpg"
  for f in "Alien (1979)" "Alien (1979) Directors Cut"; do
    video "${m}/${f}/${f}.mp4" 1 640x360
    movie_nfo "${m}/${f}" "Alien" 1979 348 tt0078748 117 Horror "Ridley Scott" "After a space merchant vessel receives an unknown transmission as a distress call, one of the crew is attacked by a mysterious life form."
    poster "${m}/${f}/poster.jpg"
  done
  video "${m}/Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4" 1 1920x1080 libx264 "" "" 24
  upscale "${m}/Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4"
  movie_nfo "${m}/Blade Runner (1982)" "Blade Runner" 1982 78 tt0083658 117 "Science Fiction" "Ridley Scott" "In the smog-choked dystopian Los Angeles of 2019, blade runner Rick Deckard is called out of retirement to terminate a quartet of replicants."
  poster "${m}/Blade Runner (1982)/poster.jpg"
  video "${m}/Interstellar (2014)/Interstellar (2014).mp4" 1 640x360
  movie_nfo "${m}/Interstellar (2014)" "Interstellar" 2014 157336 tt0816692 169 "Science Fiction" "Christopher Nolan" "The adventures of a group of explorers who make use of a newly discovered wormhole to surpass the limitations on human space travel."
  poster "${m}/Interstellar (2014)/poster.jpg"
  # what goes with a film and is not a copy of it, the three ways both
  # servers name it: a trailer named for the film, and folders of trailers
  # and extras. 360p, so an audit that took one for a film would say so
  video "${m}/Interstellar (2014)/Interstellar (2014)-trailer.mp4" 1 640x360
  video "${m}/Interstellar (2014)/Trailers/Trailer.mp4" 1 640x360
  video "${m}/Interstellar (2014)/Extras/Featurette.mp4" 1 640x360
  # a disc copied in without its structure: one VOB loose in a film's folder,
  # and nothing beside it to say what film it is
  vob "${m}/Coyote vs. Acme (2026)/VTS_01_1.VOB"
  # the same done right: a Blu-ray kept as its BDMV tree, which both servers
  # hold as one film at the folder rather than reaching in for the streams
  mkdir -p "${m}/Cube (1997)/BDMV/STREAM"
  cp "${DATA}/media/disc-src/00000.m2ts" "${DATA}/media/disc-src/00001.m2ts" "${m}/Cube (1997)/BDMV/STREAM/"
  # and a DVD kept whole: VIDEO_TS with the disc's and the title set's
  # navigation files, their backups, and the title's video, and the nfo where
  # a disc keeps one, VIDEO_TS/VIDEO_TS.nfo - the one place both servers read
  # it from (Emby passes over a movie.nfo beside the tree). The nfo names the
  # film and its plot alone: no runtime to stand in for the disc's, which
  # Emby never reads, and no genre to move the others' counts
  v="${m}/Moon (2009)/VIDEO_TS"
  ifo "${v}/VIDEO_TS.IFO" DVDVIDEO-VMG
  ifo "${v}/VIDEO_TS.BUP" DVDVIDEO-VMG
  ifo "${v}/VTS_01_0.IFO" DVDVIDEO-VTS
  ifo "${v}/VTS_01_0.BUP" DVDVIDEO-VTS
  vob "${v}/VTS_01_1.VOB"
  movie_nfo "$v" "Moon" 2009 17431 tt1182345 "" "" "" "With only three weeks left in his three-year contract, Sam Bell is eager to return to Earth. Stationed alone at a Moon-based facility with his computer assistant GERTY, an unexpected accident sets off a series of unsettling events that shake his isolation."
  mv "${v}/movie.nfo" "${v}/VIDEO_TS.nfo"
  # ids that do not hold up at TMDB: a series' IMDb id on a film (Breaking
  # Bad's, on Memento), and a TMDB id TMDB has no film for (a fan restoration
  # of Star Wars, which TMDB does not list). Each carries a plot and a poster,
  # so the id is all that is wrong with it - bar the restoration's genre,
  # spelled as no other film spells it
  video "${m}/Memento (2000)/Memento (2000).mp4" 1 640x360
  movie_nfo "${m}/Memento (2000)" "Memento" 2000 "" tt0903747 "" Mystery "" "Leonard Shelby is tracking down the man who raped and murdered his wife. The difficulty of locating his wife's killer, however, is compounded by the fact that he suffers from a rare, untreatable form of short-term memory loss."
  poster "${m}/Memento (2000)/poster.jpg"
  sw="${m}/Star Wars Episode IV - A New Hope Despecialized Edition (1977)"
  video "${sw}/Star Wars Episode IV - A New Hope Despecialized Edition (1977).mp4" 1 640x360 libx264 ger
  movie_nfo "$sw" "Star Wars: Episode IV - A New Hope (Despecialized Edition)" 1977 99999999 "" "" "Science-Fiction" "" "Princess Leia is captured and held hostage by the evil Imperial forces in their effort to take over the galactic Empire."
  poster "${sw}/poster.jpg"

  # the messy shows
  s="${DATA}/media/messy-shows"
  show_nfo "${s}/Severance" "Severance" 2022 95396 371980 tt11280740 Drama "Mark leads a team of office workers whose memories have been surgically divided between their work and personal lives."
  episode "${s}/Severance/Season 01/Severance S01E01" 1 1 "Good News About Hell" 1 640x360
  episode "${s}/Severance/Season 01/Severance S01E02" 1 2 "Half Loop" 1 640x360
  # the second episode is a DVD rip, anamorphic: its file swapped for one
  # whose 720x480 the stream says is shown at 16:9
  anamorphic "${s}/Severance/Season 01/Severance S01E02.mp4"
  episode "${s}/Severance/Season 01/Severance S01E03" 1 3 "In Perpetuity" 5 640x360
  # a season's extras, in the folder Jellyfin keeps them in as the season's
  # own; Emby 4.10 reads the file as an episode of the show with no number,
  # which the audits that judge episodes must leave alone
  video "${s}/Severance/Season 01/Extras/Featurette.mp4" 1 640x360
  episode "${s}/Star Trek The Next Generation/Season 01/Star Trek The Next Generation S01E01" 1 1 "Encounter at Farpoint" 1 640x360
  episode "${s}/Star Trek The Next Generation/Season 01/Star Trek The Next Generation S01E03" 1 3 "Code of Honor" 1 640x360
  # a show held with a whole season missing between two it has, and no ids,
  # so the gap on disk is all anyone can say of it
  d="${s}/Star Trek Deep Space Nine (1993)"
  show_nfo "$d" "Star Trek: Deep Space Nine" 1993 "" "" "" "Science Fiction" "At Deep Space Nine, a space station located next to a wormhole in the vicinity of the liberated planet of Bajor, Commander Sisko and crew welcome alien visitors, root out evildoers and solve all types of unexpected problems that come their way."
  episode "${d}/Season 01/Star Trek Deep Space Nine S01E01" 1 1 "Emissary" 1 640x360
  episode "${d}/Season 03/Star Trek Deep Space Nine S03E01" 3 1 "The Search (1)" 1 640x360
  # and its season 3 file is a broken remux: an mkv whose duration claims
  # twelve hours for a second of video, alone in its season so nothing but
  # the claim itself can say it is wrong
  rm "${d}/Season 03/Star Trek Deep Space Nine S03E01.mp4"
  claims_hours "${d}/Season 03/Star Trek Deep Space Nine S03E01.mkv"
  # a show whose file names and nfos disagree, the ways a bulk import leaves
  # them: each episode's nfo is what the server holds, the name what was placed
  p="${s}/Andor (2022)"
  show_nfo "$p" "Andor" 2022 "" "" "" "Science Fiction" "In an era filled with danger, deception and intrigue, Cassian Andor will discover the difference he can make in the struggle against the tyrannical Galactic Empire."
  episode "${p}/Season 01/Andor S01E01" 1 1 "Kassa" 1 640x360
  episode "${p}/Season 01/Andor S01E02E03" 1 2 "That Would Be Me" 1 640x360 "" 2
  episode "${p}/Season 01/Andor S01E04" 1 5 "The Axe Forgets" 1 640x360
  episode "${p}/Season 01/Breaking Bad S01E06" 1 6 "The Eye" 1 640x360
  episode "${p}/Season 01/07 - Announcement" 1 7 "Announcement" 1 640x360
  episode "${p}/Season 01/Andor S02E08" 1 8 "Narkina 5" 1 640x360
  # one show in two folders a space and a letter's case apart, no nfo in either
  video "${s}/A Knight of the Seven Kingdoms (2026)/Season 01/A Knight of the Seven Kingdoms S01E01.mp4" 1 640x360
  video "${s}/A Knight of the Seven  kingdoms (2026)/Season 01/A Knight of the Seven Kingdoms S01E02.mp4" 1 640x360
  # an OVA held on its own, known by its AniDB id alone (testdata/anime-list.xml
  # has TVDB and TMDB fold it into .hack//SIGN's specials). Its names lose the
  # title's slashes, which no file system holds, and its leading dot, which
  # makes a file hidden and both servers pass over it
  g="${s}/hack Liminality (2002)"
  mkdir -p "$g"
  {
    echo '<?xml version="1.0" encoding="utf-8"?>'
    echo '<tvshow>'
    echo '  <title>.hack//Liminality</title>'
    echo '  <year>2002</year>'
    echo '  <plot>.hack//Liminality is an OVA series directly related to the .hack video game series for the PlayStation 2, with the perspective of Liminality focused on the real world as opposed to the games'\'' MMORPG The World.</plot>'
    echo '  <genre>Science-Fiction</genre>'
    echo '  <anidbid>222</anidbid>'
    echo '  <uniqueid type="anidb">222</uniqueid>'
    echo '</tvshow>'
  } > "${g}/tvshow.nfo"
  # its episodes run three minutes but the last, cut to a second: the shortest
  # spread that clears the runtime audit's two-minute floor, since a file of a
  # second rounds to 0 minutes like its median, and small, so cheap to make
  episode "${g}/Season 01/hack Liminality S01E01" 1 1 "In the Case of Mai Minase" 180 160x90
  episode "${g}/Season 01/hack Liminality S01E02" 1 2 "In the Case of Yuki Aihara" 180 160x90
  episode "${g}/Season 01/hack Liminality S01E03" 1 3 "In the Case of Kyoko Tohno" 1 160x90

  # one show split by a folder rename: the old folder had no year, the new
  # one has, and both carry the show's ids. The second episode landed in
  # both, a 720p copy in the new folder beside the 360p one in the old
  wire="Told from the points of view of both the Baltimore homicide and narcotics detectives and their targets, the series captures a universe in which the national war on drugs has become a permanent, self-sustaining bureaucracy, and distinctions between good and evil are routinely obliterated."
  for f in "The Wire" "The Wire (2002)"; do
    show_nfo "${s}/${f}" "The Wire" 2002 1438 79126 tt0306414 Drama "$wire"
  done
  episode "${s}/The Wire/Season 01/The Wire S01E01" 1 1 "The Target" 1 640x360
  episode "${s}/The Wire/Season 01/The Wire S01E02" 1 2 "The Detail" 1 640x360
  episode "${s}/The Wire (2002)/Season 01/The Wire S01E02" 1 2 "The Detail" 1 1280x720
  episode "${s}/The Wire (2002)/Season 01/The Wire S01E03" 1 3 "The Buys" 1 640x360
  # the 2025 series held with the ids of the 1989 film its story retells:
  # TMDB's movie 11625 and its IMDb id. As a series' TMDB number 11625 is
  # another show altogether, and the IMDb id is no series at all
  x="${s}/Asterix & Obelix - The Big Fight (2025)"
  show_nfo "$x" "Asterix & Obelix: The Big Fight" 2025 11625 "" tt0096842 Animation "When their druid forgets how to prepare the magic potion, Asterix and Obelix must defend the village as Caesar plots to use a Gallic law against them."
  episode "${x}/Season 01/Asterix & Obelix - The Big Fight S01E01" 1 1 "Episode I" 1 640x360
  episode "${x}/Season 01/Asterix & Obelix - The Big Fight S01E02" 1 2 "Episode II" 1 640x360
  # a show held from its third season on: the first two are missing, which
  # nothing on disk can say
  r="${s}/Red Dwarf"
  show_nfo "$r" "Red Dwarf" 1988 326 71326 tt0094535 Comedy "The adventures of the last human alive and his friends, stranded three million years into deep space on the mining ship Red Dwarf."
  episode "${r}/Season 03/Red Dwarf S03E01" 3 1 "Backwards" 1 640x360
  episode "${r}/Season 03/Red Dwarf S03E02" 3 2 "Marooned" 1 640x360
  episode "${r}/Season 03/Red Dwarf S03E03" 3 3 "Polymorph" 1 640x360

  # the music: "<artist>/<album> (year)/NN - <title>.mp3", the shape a ripper
  # leaves behind, and "Disc N/" inside the album for one of several discs.
  # field looks a value up in one of the tables above.
  while IFS='|' read -r artist album year release art album_artist; do
    [ -n "$artist" ] || continue
    # a rip never looked up carries neither MusicBrainz id
    mbartist=""
    [ -z "$release" ] || mbartist=$(field "$ARTISTS" 1 "$artist" 2)
    genre=$(field "$ARTISTS" 1 "$artist" 3)
    dir="${DATA}/media/music/${artist}/${album} (${year})"
    # the art goes in twice, the way a ripper leaves it: beside the tracks and
    # embedded in each of them. Emby's albums have no path, so only the
    # embedded picture ever reaches them
    art_file=""
    if [ "$art" = "cover" ]; then
      cover "${dir}/cover.jpg"
      art_file="${dir}/cover.jpg"
    fi
    discs=$(printf '%s\n' "$TRACKS" | awk -F'|' -v a="$artist" -v b="$album" '$1 == a && $2 == b && $3 ~ /-/ { split($3, p, "-"); if (p[1] > n) n = p[1] } END { print n + 0 }')
    while IFS='|' read -r t_artist t_album position title wrong; do
      [ "$t_artist" = "$artist" ] && [ "$t_album" = "$album" ] || continue
      disc="" track=$position sub=""
      case "$position" in
        *-*) disc=${position%%-*} track=${position#*-} sub="Disc ${disc}/" ;;
      esac
      # the tracks on this one's disc, which is what its track number counts
      of=$(printf '%s\n' "$TRACKS" | awk -F'|' -v a="$artist" -v b="$album" -v d="$disc" '$1 == a && $2 == b && (d == "" || index($3, d "-") == 1)' | wc -l | tr -d ' ')
      tag_track=$track tag_artist=$artist tag_album=$album
      case "$wrong" in
        track=*) tag_track=${wrong#track=} ;;
        artist=*) tag_artist=${wrong#artist=} ;;
        album=*) tag_album=${wrong#album=} ;;
      esac
      audio "$(printf '%s/%s%02d - %s.mp3' "$dir" "$sub" "$track" "$title")" \
        "$title" "$tag_track" "$of" "$tag_artist" "$tag_album" "$year" "$genre" "$mbartist" "$release" "$art_file" \
        "${album_artist:-$artist}" "${disc:+${disc}/${discs}}"
    done <<<"$TRACKS"
  done <<<"$ALBUMS"

  # one track gets an .lrc beside it: Jellyfin reads lyrics from a sidecar
  # with no plugin, and nothing else in the fixtures has any
  lyrics "${DATA}/media/music/SirensCeol/Afterworld (2016)/02 - The Future We Built.lrc"

  mkdir -p "${DATA}/config" "${DATA}/cache"
  # Emby runs as uid 2 and Jellyfin as root; both have to read the media and
  # write their config, and this tree is a throwaway
  chmod -R 777 "${DATA}"
}

# proxy_ca mints the certificate authority the tests' provider proxy signs
# with, once, so every container started here trusts the same one. The tests
# load these files (providerproxy.Options.CACert/CAKey) rather than minting
# their own.
proxy_ca() {
  [ -f "${PROXY_CA}/ca.pem" ] && [ -f "${PROXY_CA}/ca.key" ] && return 0
  command -v openssl >/dev/null || { echo "openssl is required to mint the provider proxy CA" >&2; exit 1; }
  log "minting the provider proxy CA under ${PROXY_CA}"
  mkdir -p "${PROXY_CA}"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
    -subj "/CN=embyfin-mcp provider proxy CA" \
    -keyout "${PROXY_CA}/ca.key" -out "${PROXY_CA}/ca.pem" 2>/dev/null
  chmod 644 "${PROXY_CA}/ca.pem"
}

# retry TRIES COMMAND... - run a command until it succeeds, for the wizard
# steps: Jellyfin answers its first requests with a 404 or 503 for a moment
# after it starts listening, while the rest of the app comes up
retry() {
  local tries=$1
  shift
  for _ in $(seq "$tries"); do
    if "$@"; then return 0; fi
    sleep 2
  done
  echo "giving up on: $*" >&2
  return 1
}

# wait_for WHAT TRIES COMMAND
wait_for() {
  local what=$1 tries=$2 cmd=$3
  log "waiting for ${what}"
  for _ in $(seq "$tries"); do
    if eval "$cmd" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  echo "timed out waiting for ${what}" >&2
  docker logs "$NAME" 2>&1 | tail -40 >&2
  return 1
}

# logs prints what the server itself wrote. Emby logs to
# /config/logs/embyserver.txt rather than stdout, so `docker logs` shows
# almost nothing of what went wrong; Jellyfin writes /config/log/*.log.
logs() {
  echo "==> docker logs ${NAME}" >&2
  docker logs "$NAME" 2>&1 | tail -40 >&2
  for f in "${DATA}"/config/logs/*.txt "${DATA}"/config/log/*.log; do
    [ -f "$f" ] || continue
    # the errors first: the tail of an Emby log is its codec report, and what
    # went wrong is usually hundreds of lines above it
    echo "==> ${f} (errors)" >&2
    grep -iE 'error|warn|exception|cancel|refused|timed out|certificate' "$f" | tail -60 >&2
    echo "==> ${f} (tail)" >&2
    tail -40 "$f" >&2
  done
}

up() {
  command -v ffmpeg >/dev/null || { echo "ffmpeg is required to generate fixtures" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  command -v docker >/dev/null || { echo "docker is required" >&2; exit 1; }

  down >/dev/null 2>&1 || true
  fixtures
  proxy_ca

  local proxy_at
  proxy_at="$(proxy_host)"
  log "starting ${IMAGE} as ${NAME} on ${PORT} (providers proxied via ${proxy_at}:${PROXY_PORT})"
  local mounts=(-v "${DATA}/media:/media" -v "${DATA}/config:/config" -v "${PROXY_CA}/ca.pem:/proxy/ca.pem:ro")
  [ "$BACKEND" = "jellyfin" ] && mounts+=(-v "${DATA}/cache:/cache")
  # NO_PROXY carries the container's own name: Emby pings itself over HTTP
  # on startup, and that has no business going through the proxy
  docker run -d --name "$NAME" \
    -p "${PORT}:8096" \
    --hostname "$NAME" \
    --add-host "host.docker.internal:host-gateway" \
    -e "HTTP_PROXY=http://${proxy_at}:${PROXY_PORT}" \
    -e "HTTPS_PROXY=http://${proxy_at}:${PROXY_PORT}" \
    -e "http_proxy=http://${proxy_at}:${PROXY_PORT}" \
    -e "https_proxy=http://${proxy_at}:${PROXY_PORT}" \
    -e "NO_PROXY=localhost,127.0.0.1,${NAME}" \
    -e "SSL_CERT_FILE=/proxy/ca.pem" \
    "${mounts[@]}" \
    "$IMAGE" >/dev/null

  # /System/Info/Public answers before the wizard endpoints do, so wait on
  # the first endpoint the setup actually uses
  wait_for "the server to answer /Startup/User" 90 "curl -fsS ${URL}/Startup/User"

  # the first-run wizard, the way the web client drives it: locale, the
  # first user, then Complete. Emby creates the first user before the wizard
  # (named after the container's uid) and POST /Startup/User renames it;
  # Jellyfin creates it from the same call.
  log "completing the setup wizard"
  retry 15 api POST /Startup/Configuration '{"UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"} ' >/dev/null
  retry 15 api POST /Startup/User "$(jq -n --arg u "$ADMIN" --arg p "$PASSWORD" '{Name: $u, Password: $p}')" >/dev/null
  if [ "$BACKEND" = "jellyfin" ]; then
    retry 15 api POST /Startup/RemoteAccess '{"EnableRemoteAccess":true,"EnableAutomaticPortMapping":false}' >/dev/null
  fi
  retry 15 api POST /Startup/Complete >/dev/null

  log "logging in"
  local login access user_id
  login=$(curl -fsS -X POST "${URL}/Users/AuthenticateByName" \
    -H 'Content-Type: application/json' \
    -H 'X-Emby-Authorization: MediaBrowser Client="embyfin-mcp-testenv", Device="testenv", DeviceId="testenv", Version="0"' \
    -H 'Authorization: MediaBrowser Client="embyfin-mcp-testenv", Device="testenv", DeviceId="testenv", Version="0"' \
    -d "$(jq -n --arg u "$ADMIN" --arg p "$PASSWORD" '{Username: $u, Pw: $p}')")
  access=$(echo "$login" | jq -r '.AccessToken')
  user_id=$(echo "$login" | jq -r '.User.Id')
  [ "$access" != "null" ] || { echo "no AccessToken in the login response" >&2; exit 1; }

  # an API key is what embyfin-mcp is meant to run on, so the tests
  # authenticate through the same path as production rather than with a
  # session token
  log "creating an API key"
  api POST "/Auth/Keys?app=embyfin-mcp-test" "" "$access" >/dev/null
  local key
  key=$(api GET /Auth/Keys "" "$access" | jq -r '.Items[] | select(.AppName == "embyfin-mcp-test") | .AccessToken' | head -1)
  [ -n "$key" ] && [ "$key" != "null" ] || { echo "no API key in the response" >&2; exit 1; }

  # a second, ordinary user, so user_list has two rows and the user tools
  # can be pointed at someone who is not the key's own account
  log "creating user ${USER2}"
  local u2
  if [ "$BACKEND" = "jellyfin" ]; then
    u2=$(api POST /Users/New "$(jq -n --arg u "$USER2" --arg p "$PASSWORD" '{Name: $u, Password: $p}')" "$key" | jq -r .Id)
  else
    u2=$(api POST /Users/New "$(jq -n --arg u "$USER2" '{Name: $u}')" "$key" | jq -r .Id)
    api POST "/Users/${u2}/Password" "$(jq -n --arg p "$PASSWORD" '{NewPw: $p}')" "$key" >/dev/null
  fi
  [ -n "$u2" ] && [ "$u2" != "null" ] || { echo "could not create ${USER2}" >&2; exit 1; }

  # consumed with eval "$(scripts/testenv.sh up)"
  echo "export EMBYFIN_BACKEND='${BACKEND}'"
  echo "export EMBYFIN_SERVER='${URL}'"
  echo "export EMBYFIN_TOKEN='${key}'"
  # the tests create the libraries themselves with library_create over these
  # container-side paths, and add files under EMBYFIN_TEST_DATA to prove
  # library_scan picks them up
  echo "export EMBYFIN_TEST_DATA='${DATA}'"
  echo "export EMBYFIN_TEST_PROXY_PORT='${PROXY_PORT}'"
  echo "export EMBYFIN_TEST_PROXY_CA='${PROXY_CA}'"
  echo "export EMBYFIN_TEST_ADMIN='${ADMIN}'"
  echo "export EMBYFIN_TEST_ADMIN_ID='${user_id}'"
  echo "export EMBYFIN_TEST_USER='${USER2}'"
  echo "export EMBYFIN_TEST_USER_ID='${u2}'"
  echo "export EMBYFIN_TEST_PASSWORD='${PASSWORD}'"
}

down() {
  log "removing ${NAME}"
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  wipe_data
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  fixtures) fixtures ;;  # generate the media tree only, for inspecting the layout
  logs) logs ;;          # what the server wrote about itself, for a failing run
  image) echo "$IMAGE" ;; # the image this backend runs, for a docker pull
  *) echo "usage: EMBYFIN_TEST_BACKEND=emby|jellyfin $0 [up|down|fixtures|logs|image]" >&2; exit 1 ;;
esac
