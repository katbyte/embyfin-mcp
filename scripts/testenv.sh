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
The Thirteenth Floor (1999)|The Thirteenth Floor|1999|1090|tt0139809|100|Science Fiction|Josef Rusnak|When his mentor is murdered, a computer scientist enters the simulation of 1937 Los Angeles they built together to find the killer, and begins to doubt that his own world is real.'

# folder|title|year|tmdb|tvdb|imdb|genre|plot
SHOWS='Severance|Severance|2022|95396|371980|tt11280740|Drama|Mark leads a team of office workers whose memories have been surgically divided between their work and personal lives.
Breaking Bad|Breaking Bad|2008|1396|81189|tt0903747|Drama|A chemistry teacher diagnosed with cancer turns to manufacturing methamphetamine to secure his family'\''s future.
The Expanse|The Expanse|2015|63639|280619|tt3230854|Science Fiction|A police detective in the asteroid belt, the first officer of an interplanetary ice freighter and an earth-bound UN executive slowly discover a vast conspiracy.'

# show folder|season|episode|title - the episode files on disk. Severance
# season one has nine episodes and we hold two, so with the provider on the
# server lists the rest as missing; Breaking Bad holds three of seven.
EPISODES='Severance|1|1|Good News About Hell
Severance|1|2|Half Loop
Severance|2|1|Hello, Ms. Cobel
Severance|2|2|Goodbye, Mrs. Selvig
Breaking Bad|1|1|Pilot
Breaking Bad|1|2|Cat'\''s in the Bag...
Breaking Bad|1|3|...And the Bag'\''s in the River
The Expanse|1|1|Dulcinea
The Expanse|1|2|The Big Empty'

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
# artist|musicbrainz artist id|genre
ARTISTS='Battle Tapes|82178603-e97b-4d60-b521-82582545a0a8|Electronic
Coyote Kisses|09c08ae4-0b3e-4e06-9892-c6a1ec3c9d6c|Electronic
Pink Floyd|83d91898-7763-47d7-b03b-b92132375c47|Progressive Rock
SirensCeol|621dac65-5eac-4ad3-a630-05f282bbe4e2|Electronica'

# artist|album|year|musicbrainz release id|cover or nocover
ALBUMS='Battle Tapes|Polygon|2015|0e715a3a-8461-4620-8843-6c4324c64d49|cover
Coyote Kisses|Thundercolor|2013|aa89ebe0-8c11-427d-8929-46812063be90|nocover
Pink Floyd|The Dark Side of the Moon|1973|b84ee12a-09ef-421b-82de-0441a926375b|cover
Pink Floyd|Wish You Were Here|1975|f4a8aa35-da90-33d8-9307-c630d38a2bed|cover
SirensCeol|Afterworld|2016|00cc6656-b7c3-4c33-8df3-5909e46979b2|cover'

# artist|album|track number|title - the first four of each album
TRACKS='Battle Tapes|Polygon|1|Belgrade
Battle Tapes|Polygon|2|Valkyrie
Battle Tapes|Polygon|3|Solid Gold
Battle Tapes|Polygon|4|Private Dancer
Coyote Kisses|Thundercolor|1|Diving At Night
Coyote Kisses|Thundercolor|2|Stay With You
Coyote Kisses|Thundercolor|3|This Is How You Know
Coyote Kisses|Thundercolor|4|Changing Guard
Pink Floyd|The Dark Side of the Moon|1|Speak to Me
Pink Floyd|The Dark Side of the Moon|2|Breathe
Pink Floyd|The Dark Side of the Moon|3|On the Run
Pink Floyd|The Dark Side of the Moon|4|Time
Pink Floyd|Wish You Were Here|1|Shine On You Crazy Diamond, Parts I-V
Pink Floyd|Wish You Were Here|2|Welcome to the Machine
Pink Floyd|Wish You Were Here|3|Have a Cigar
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
#   Princess Mononoke (1997)       no nfo at all: unmatched, no overview, no poster; and MPEG-4
#                                  part 2 video, the legacy codec audit_quality looks for
#   Arrival (2016)                 nfo with ids but no plot, and no poster
#   Dune (2021)                    folder says 2021, nfo says 1984 (David Lynch's film, the other
#                                  Dune): year mismatch
#   Alien (1979) + Directors Cut   two folders, one tmdb id: duplicates (and a third copy sits in the clean library)
#   Blade Runner (1982)            two files in one folder, really 1080p and 2160p (the only messy files
#                                  audit_quality leaves alone): multiple versions on
#                                  Jellyfin, which folds them into one entry; two films on Emby, which
#                                  only merges versions from its web client (so a duplicate there)
#   Interstellar (2014)            nfo says 169 minutes, the file runs one second: runtime off
# messy-shows:
#   Severance                      season one, three episodes, the third five seconds long: runtime outlier
#   Star Trek The Next Generation  no tvshow.nfo: unmatched; episodes 1 and 3, no 2

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

# video PATH [SECONDS] [SIZE] [CODEC] - a short file of that length, 720p
# h264 unless a size and codec say otherwise, so the scanner has a real
# container to probe and a runtime to report. At five frames a second a 720p
# second is tens of kilobytes and a 2160p one under a megabyte.
video() {
  mkdir -p "$(dirname "$1")"
  # -nostdin matters: without it ffmpeg reads the while-read loop's stdin
  # looking for interactive keys and swallows a character of the next line,
  # which silently truncates the titles that follow
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "testsrc2=s=${3:-1280x720}:r=5" -f lavfi -i "anullsrc=r=22050:cl=mono" \
    -t "${2:-1}" -c:v "${4:-libx264}" -pix_fmt yuv420p -c:a aac -shortest "$1"
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

# audio FILE TITLE TRACK OF ARTIST ALBUM YEAR GENRE MBARTIST MBALBUM [COVER] -
# a one-second tone tagged as the track it stands for, with COVER embedded as
# its album art the way a ripper leaves it. The servers read a song from its
# tags, not its name, so this is what they have to go on with the fetchers
# off; the MusicBrainz ids go in as the TXXX frames every ripper writes.
audio() {
  local file=$1 title=$2 track=$3 of=$4 artist=$5 album=$6 year=$7 genre=$8 mbartist=$9 mbalbum=${10} art=${11:-}
  mkdir -p "$(dirname "$file")"
  local art_in=() art_map=()
  if [ -n "$art" ]; then
    art_in=(-i "$art")
    art_map=(-map 1:v -c:v mjpeg -disposition:v attached_pic
      -metadata:s:v "title=Album cover" -metadata:s:v "comment=Cover (front)")
  fi
  ffmpeg -nostdin -loglevel error -y \
    -f lavfi -i "sine=frequency=$((220 + track * 55)):duration=1:sample_rate=44100" \
    "${art_in[@]}" -map 0:a "${art_map[@]}" \
    -c:a libmp3lame -b:a 64k -id3v2_version 3 \
    -metadata "title=${title}" \
    -metadata "track=${track}/${of}" \
    -metadata "artist=${artist}" \
    -metadata "album_artist=${artist}" \
    -metadata "album=${album}" \
    -metadata "date=${year}" \
    -metadata "genre=${genre}" \
    -metadata "MusicBrainz Artist Id=${mbartist}" \
    -metadata "MusicBrainz Album Id=${mbalbum}" \
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

# episode FILE_BASE SEASON EPISODE TITLE [SECONDS] [SIZE] - one episode with its nfo.
episode() {
  local base=$1 season=$2 ep=$3 title=$4 secs=${5:-1} size=${6:-1280x720}
  video "${base}.mp4" "$secs" "$size"
  {
    echo '<?xml version="1.0" encoding="utf-8"?>'
    echo '<episodedetails>'
    echo "  <title>$(xml_escape "$title")</title>"
    echo "  <season>${season}</season>"
    echo "  <episode>${ep}</episode>"
    echo "  <plot>Episode ${ep} of season ${season}: $(xml_escape "$title").</plot>"
    echo '</episodedetails>'
  } > "${base}.nfo"
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

  # the clean shows: "<show>/Season NN/<show> SNNENN.mp4" with an nfo beside each
  while IFS='|' read -r folder title year tmdb tvdb imdb genre plot; do
    [ -n "$folder" ] || continue
    dir="${DATA}/media/shows/${folder}"
    show_nfo "$dir" "$title" "$year" "$tmdb" "$tvdb" "$imdb" "$genre" "$plot"
    poster "${dir}/poster.jpg"
  done <<<"$SHOWS"
  while IFS='|' read -r show season ep title; do
    [ -n "$show" ] || continue
    episode "$(printf '%s/media/shows/%s/Season %02d/%s S%02dE%02d' "$DATA" "$show" "$season" "$show" "$season" "$ep")" "$season" "$ep" "$title"
  done <<<"$EPISODES"

  # the messy movies
  m="${DATA}/media/messy-movies"
  video "${m}/Princess Mononoke (1997)/Princess Mononoke (1997).mp4" 1 640x360 mpeg4
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
  video "${m}/Blade Runner (1982)/Blade Runner (1982) - 1080p.mp4" 1 1920x1080
  video "${m}/Blade Runner (1982)/Blade Runner (1982) - 2160p.mp4" 1 3840x2160
  movie_nfo "${m}/Blade Runner (1982)" "Blade Runner" 1982 78 tt0083658 117 "Science Fiction" "Ridley Scott" "In the smog-choked dystopian Los Angeles of 2019, blade runner Rick Deckard is called out of retirement to terminate a quartet of replicants."
  poster "${m}/Blade Runner (1982)/poster.jpg"
  video "${m}/Interstellar (2014)/Interstellar (2014).mp4" 1 640x360
  movie_nfo "${m}/Interstellar (2014)" "Interstellar" 2014 157336 tt0816692 169 "Science Fiction" "Christopher Nolan" "The adventures of a group of explorers who make use of a newly discovered wormhole to surpass the limitations on human space travel."
  poster "${m}/Interstellar (2014)/poster.jpg"

  # the messy shows
  s="${DATA}/media/messy-shows"
  show_nfo "${s}/Severance" "Severance" 2022 95396 371980 tt11280740 Drama "Mark leads a team of office workers whose memories have been surgically divided between their work and personal lives."
  episode "${s}/Severance/Season 01/Severance S01E01" 1 1 "Good News About Hell" 1 640x360
  episode "${s}/Severance/Season 01/Severance S01E02" 1 2 "Half Loop" 1 640x360
  episode "${s}/Severance/Season 01/Severance S01E03" 1 3 "In Perpetuity" 5 640x360
  episode "${s}/Star Trek The Next Generation/Season 01/Star Trek The Next Generation S01E01" 1 1 "Encounter at Farpoint" 1 640x360
  episode "${s}/Star Trek The Next Generation/Season 01/Star Trek The Next Generation S01E03" 1 3 "Code of Honor" 1 640x360

  # the music: "<artist>/<album> (year)/NN - <title>.mp3", the shape a ripper
  # leaves behind. field looks a value up in one of the tables above.
  while IFS='|' read -r artist album year release art; do
    [ -n "$artist" ] || continue
    mbartist=$(field "$ARTISTS" 1 "$artist" 2)
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
    of=$(printf '%s\n' "$TRACKS" | awk -F'|' -v a="$artist" -v b="$album" '$1 == a && $2 == b' | wc -l | tr -d ' ')
    while IFS='|' read -r t_artist t_album track title; do
      [ "$t_artist" = "$artist" ] && [ "$t_album" = "$album" ] || continue
      audio "$(printf '%s/%02d - %s.mp3' "$dir" "$track" "$title")" \
        "$title" "$track" "$of" "$artist" "$album" "$year" "$genre" "$mbartist" "$release" "$art_file"
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

  log "starting ${IMAGE} as ${NAME} on ${PORT} (providers proxied via host.docker.internal:${PROXY_PORT})"
  local mounts=(-v "${DATA}/media:/media" -v "${DATA}/config:/config" -v "${PROXY_CA}/ca.pem:/proxy/ca.pem:ro")
  [ "$BACKEND" = "jellyfin" ] && mounts+=(-v "${DATA}/cache:/cache")
  # NO_PROXY carries the container's own name: Emby pings itself over HTTP
  # on startup, and that has no business going through the proxy
  docker run -d --name "$NAME" \
    -p "${PORT}:8096" \
    --hostname "$NAME" \
    --add-host "host.docker.internal:host-gateway" \
    -e "HTTP_PROXY=http://host.docker.internal:${PROXY_PORT}" \
    -e "HTTPS_PROXY=http://host.docker.internal:${PROXY_PORT}" \
    -e "http_proxy=http://host.docker.internal:${PROXY_PORT}" \
    -e "https_proxy=http://host.docker.internal:${PROXY_PORT}" \
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
  *) echo "usage: EMBYFIN_TEST_BACKEND=emby|jellyfin $0 [up|down|fixtures|logs]" >&2; exit 1 ;;
esac
