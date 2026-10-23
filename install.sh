#!/bin/sh
# owrt-bot installer and updater for OpenWrt.
#
#   curl -fsSL https://raw.githubusercontent.com/lastharbor/owrt-bot/main/install.sh | sh
#
# Non-interactive:   ... | BOT_TOKEN=123:abc CHAT_ID=111 sh
# Specific version:  ... | OWRT_BOT_VERSION=v0.2.0 sh
# Mirror / offline:  ... | OWRT_BOT_BASE_URL=https://mirror.example/owrt-bot sh
#                    (a directory with owrt-bot-linux-<arch> and checksums.txt)
# Uninstall:         ... | sh -s -- uninstall        (keeps /etc/owrt-bot)
#                    ... | sh -s -- purge            (also removes settings)
#
# Re-running the install command upgrades the bot and keeps its settings.
set -eu

REPO=lastharbor/owrt-bot
VERSION=${OWRT_BOT_VERSION:-latest}
BIN=/usr/bin/owrt-bot
INIT=/etc/init.d/owrt-bot
DATA=/etc/owrt-bot
CONF=$DATA/config.conf
LOG=/var/log/owrt-bot.log
LEGACY_CONF=/etc/owrt-tg-bot/config.conf
TMP=$(mktemp -d /tmp/owrt-bot-install.XXXXXX)
trap 'rm -rf "$TMP"' EXIT INT TERM

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31mx\033[0m %s\n' "$*" >&2; exit 1; }

# fetch URL FILE: curl, then OpenWrt's uclient-fetch, then wget.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 2 -o "$2" "$1"
	elif command -v uclient-fetch >/dev/null 2>&1; then
		uclient-fetch -q -O "$2" "$1"
	else
		wget -qO "$2" "$1"
	fi
}

# ask VAR "prompt": read from the terminal even though stdin is the script.
ask() {
	[ -r /dev/tty ] || die "нет терминала для вопросов: передай BOT_TOKEN и CHAT_ID переменными окружения"
	printf '%s' "$2" >/dev/tty
	IFS= read -r "$1" </dev/tty
}

detect_arch() {
	pkgarch=$( (opkg print-architecture 2>/dev/null || true) | awk '$2 !~ /^(all|noarch)$/ {a=$2} END {print a}')
	[ -n "$pkgarch" ] || pkgarch=$(uname -m)
	case "$pkgarch" in
		aarch64*|arm64*) echo arm64 ;;
		x86_64*|amd64*)  echo amd64 ;;
		arm*)            echo armv7 ;;
		mipsel*|mipsle*) echo mipsle ;;
		mips*)           echo mips ;;
		*) die "архитектура не поддерживается: $pkgarch" ;;
	esac
}

write_init() {
	cat >"$INIT" <<'EOF'
#!/bin/sh /etc/rc.common
# procd service for owrt-bot, written by install.sh

START=95
STOP=10
USE_PROCD=1

start_service() {
	[ -f /etc/owrt-bot/config.conf ] || return 1
	procd_open_instance
	procd_set_param command /usr/bin/owrt-bot -config /etc/owrt-bot/config.conf
	procd_set_param env GOMEMLIMIT=48MiB GOGC=50
	procd_set_param respawn 3600 5 0
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_set_param file /etc/owrt-bot/config.conf
	procd_close_instance
}
EOF
	chmod 755 "$INIT"
}

# tg METHOD [query]: call the Bot API with the token being set up.
tg() { fetch "https://api.telegram.org/bot$TOKEN/$1${2:+?$2}" "$TMP/tg.json" && cat "$TMP/tg.json"; }

json() { jsonfilter -i "$TMP/tg.json" -e "$1" 2>/dev/null || true; }

setup_config() {
	if [ -f "$CONF" ]; then
		say "настройки уже есть, оставляю $CONF"
		return
	fi
	mkdir -p "$DATA" && chmod 700 "$DATA"
	if [ -f "$LEGACY_CONF" ]; then
		cp "$LEGACY_CONF" "$CONF" && chmod 600 "$CONF"
		say "перенёс настройки старого owrt-tg-bot ($LEGACY_CONF)"
		return
	fi

	TOKEN=${BOT_TOKEN:-}
	while :; do
		[ -n "$TOKEN" ] || ask TOKEN "Токен бота от @BotFather: "
		if printf '%s' "$TOKEN" | grep -Eq '^[0-9]+:[A-Za-z0-9_-]{30,}$' && tg getMe >/dev/null 2>&1; then
			say "токен принят: @$(json '@.result.username')"
			break
		fi
		warn "Telegram не принял токен, попробуй ещё раз"
		[ -z "${BOT_TOKEN:-}" ] || die "BOT_TOKEN неверный"
		TOKEN=
	done

	CHATS=${CHAT_ID:-}
	if [ -z "$CHATS" ]; then
		say "напиши своему боту в Telegram любое сообщение (жду до 2 минут)..."
		tg deleteWebhook >/dev/null 2>&1 || true
		tg getUpdates "offset=-1" >/dev/null 2>&1 || true
		last=$(json '@.result[-1].update_id')
		i=0
		while [ $i -lt 24 ] && [ -z "$CHATS" ]; do
			tg getUpdates "timeout=5&offset=$(( ${last:-0} + 1 ))" >/dev/null 2>&1 || true
			CHATS=$(json '@.result[-1].message.chat.id')
			i=$((i + 1))
		done
		if [ -n "$CHATS" ]; then
			say "твой chat id: $CHATS ($(json '@.result[-1].message.from.first_name'))"
		else
			ask CHATS "Сообщение не пришло. Введи chat id (его покажет @userinfobot): "
		fi
	fi
	printf '%s' "$CHATS" | grep -Eq '^-?[0-9]+(,-?[0-9]+)*$' || die "chat id должен быть числом"

	umask 077
	cat >"$CONF" <<EOF
[telegram]
bot_token=$TOKEN
allowed_chat_ids=$CHATS

[general]
language=ru

[alerts]
enabled=1
cpu_percent=90
mem_percent=90
disk_percent=90
temp_celsius=80
check_interval_sec=60
wan_check_host=1.1.1.1

[logging]
max_size_kb=256
level=info

[bot]
lan_iface=br-lan
EOF
	chmod 600 "$CONF"
	say "настройки записаны в $CONF"
}

stop_legacy() {
	if [ -x /etc/init.d/owrt-tg-bot ]; then
		say "останавливаю старый Lua-бот (два бота на одном токене мешают друг другу)"
		/etc/init.d/owrt-tg-bot stop >/dev/null 2>&1 || true
		/etc/init.d/owrt-tg-bot disable >/dev/null 2>&1 || true
	fi
}

uninstall() {
	[ -x "$INIT" ] && { "$INIT" stop || true; "$INIT" disable || true; }
	rm -f "$BIN" "$INIT"
	nft delete table inet owrt_bot 2>/dev/null || true
	if [ "${1:-}" = purge ]; then
		rm -rf "$DATA" "$LOG" "$LOG.1"
		sed -i '\|^/etc/owrt-bot/$|d' /etc/sysupgrade.conf 2>/dev/null || true
		say "owrt-bot удалён вместе с настройками"
	else
		say "owrt-bot удалён, настройки оставлены в $DATA"
	fi
	exit 0
}

main() {
	[ "$(id -u)" = 0 ] || die "запускай от root"
	[ -f /etc/openwrt_release ] || die "это не OpenWrt"
	case "${1:-}" in
		uninstall) uninstall ;;
		purge) uninstall purge ;;
	esac

	arch=$(detect_arch)
	if [ -n "${OWRT_BOT_BASE_URL:-}" ]; then
		base=$OWRT_BOT_BASE_URL # mirror or local test: a directory with the release files
	elif [ "$VERSION" = latest ]; then
		base="https://github.com/$REPO/releases/latest/download"
	else
		base="https://github.com/$REPO/releases/download/$VERSION"
	fi
	asset="owrt-bot-linux-$arch"
	say "скачиваю $asset ($VERSION)"
	fetch "$base/$asset" "$TMP/$asset" || die "не скачалось: $base/$asset"
	fetch "$base/checksums.txt" "$TMP/checksums.txt" || die "не скачалось: checksums.txt"
	want=$(awk -v f="$asset" '{n = $2; sub(/^[*]/, "", n)} n == f {print $1}' "$TMP/checksums.txt")
	got=$(sha256sum "$TMP/$asset" | awk '{print $1}')
	[ -n "$want" ] && [ "$want" = "$got" ] || die "контрольная сумма $asset не совпала"
	chmod 755 "$TMP/$asset"
	"$TMP/$asset" -version >/dev/null 2>&1 || die "бинарник не запускается на этом роутере"

	setup_config
	stop_legacy

	[ -x "$INIT" ] && "$INIT" stop >/dev/null 2>&1 || true
	mv "$TMP/$asset" "$BIN"
	write_init
	grep -qx '/etc/owrt-bot/' /etc/sysupgrade.conf 2>/dev/null || echo '/etc/owrt-bot/' >>/etc/sysupgrade.conf
	"$INIT" enable
	before=$(wc -l <"$LOG" 2>/dev/null || echo 0)
	"$INIT" start

	i=0
	while [ $i -lt 15 ]; do
		if tail -n +"$((before + 1))" "$LOG" 2>/dev/null | grep -q "polling started"; then
			say "owrt-bot $("$BIN" -version) запущен. Открой бота в Telegram и отправь /start"
			exit 0
		fi
		sleep 1
		i=$((i + 1))
	done
	warn "бот не отчитался о запуске за 15 с, последние строки лога:"
	tail -n 10 "$LOG" 2>/dev/null || logread -e owrt-bot | tail -n 10
	exit 1
}

main "$@"
