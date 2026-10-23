# owrt-bot

Telegram-бот для роутера на OpenWrt: Wake-on-LAN с живыми статусами,
мониторинг роутера, клиенты, Wi-Fi, бэкапы. Один статический бинарник на Go,
одно постоянное HTTP/2-соединение с Telegram, кнопки отвечают за ~0,3 с.

## Как выглядит

<table>
<tr>
<td><img src="docs/img/tg-menu.png" width="260" alt="Главное меню"></td>
<td><img src="docs/img/tg-system.png" width="260" alt="Система: живые CPU, память, диск, службы"></td>
<td><img src="docs/img/tg-network.png" width="260" alt="Сеть: скорость WAN с графиками"></td>
</tr>
<tr>
<td><img src="docs/img/tg-wol.png" width="260" alt="Пробуждение: устройства со статусами"></td>
<td><img src="docs/img/tg-waking.png" width="260" alt="Будим ноутбук, идёт отсчёт"><br><img src="docs/img/tg-woke.png" width="260" alt="Ноутбук в сети через 12 с"></td>
<td><img src="docs/img/tg-device.png" width="260" alt="Карточка устройства с выключением по SSH"></td>
</tr>
<tr>
<td><img src="docs/img/tg-group.png" width="260" alt="Группа устройств"></td>
<td><img src="docs/img/tg-schedule.png" width="260" alt="Расписание пробуждения"></td>
<td><img src="docs/img/tg-packages.png" width="260" alt="Пакеты: обновления"></td>
</tr>
</table>

Настройка в LuCI (**Службы → Telegram-бот**):

<img src="docs/img/luci.png" width="640" alt="Страница настроек в LuCI">

<sub>Экраны Telegram сняты с настоящего вывода бота на демо-данных (`tools/screens`), оформление клиента нарисовано.</sub>

## Установка

На роутере под root:

```sh
curl -fsSL https://raw.githubusercontent.com/lastharbor/owrt-bot/main/install.sh | sh
```

Скрипт определит архитектуру, скачает бинарник из
[релизов](https://github.com/lastharbor/owrt-bot/releases) и сверит sha256.
Токен спросит сам и проверит через Telegram, а твой chat id узнает, когда ты
напишешь боту любое сообщение. Если раньше стоял Lua-бот `owrt-tg-bot`,
скрипт заберёт его настройки и выключит его.

Обновление: та же команда. Настройки и устройства сохраняются.

Без вопросов (например, из своего скрипта):

```sh
curl -fsSL https://raw.githubusercontent.com/lastharbor/owrt-bot/main/install.sh | BOT_TOKEN=123:abc CHAT_ID=111111 sh
```

Конкретная версия: `OWRT_BOT_VERSION=v0.2.0`. Удаление: `| sh -s -- uninstall`
(настройки остаются) или `| sh -s -- purge` (вместе с настройками).

Поддерживаются `aarch64`, `x86_64`, `armv7`, `mipsel`, `mips`. Если на роутере
нет `curl`, подойдёт `uclient-fetch -qO- <url> | sh`.

## Настройка в LuCI

Если на роутере есть LuCI, установщик добавляет страницу **Службы → Telegram-бот**:

- токен бота и кому отвечать;
- **Кто писал боту**: напиши боту любое сообщение, обнови страницу и нажми
  «Разрешить», искать chat id не нужно;
- пороги уведомлений: CPU, память, диск, температура;
- службы для экрана «Система»: галочками из списка служб роутера;
- «Проверить токен», «Перезапустить», лог.

«Сохранить и применить» перезапускает бота сам.

Настройки лежат в UCI, `/etc/config/owrt-bot`, их можно менять и через `uci`.
Устройства, группы и расписание пробуждения настраиваются в самом боте.

## Что умеет

### Пробуждение (Wake-on-LAN)

- список устройств со статусом 🟢 в сети, 🟡 спит/молчит, 🔴 выключен, ⚪ нет IP;
  экран обновляется сам;
- «Разбудить» с таймером в сообщении: «⏳ 14 с» → «🟢 в сети через 23 с»;
- добавление одним экраном из DHCP, статических адресов и ARP или вручную;
- переименование, смена IP/MAC, удаление с подтверждением;
- группы («разбудить всё»), расписание по дням недели;
- уведомления «включился / выключился»;
- выключение, перезагрузка и сон ПК по SSH;
- команды `/wake имя|группа|все`, `/status`, `/wol`.

### Роутер

Живые CPU (с графиком), память, диск, температура, аптайм,
скорость WAN с графиками, ping, внешний IP, тест скорости (4 потока,
запасные серверы: Cloudflare, OVH, Selectel), статус и
перезапуск служб, перезагрузка; клиенты DHCP с временной блокировкой (nftables);
Wi-Fi (если есть радио); пакеты к обновлению; бэкап и восстановление
`sysupgrade`; живой лог; русский и английский интерфейс.

Чат остаётся чистым: бот редактирует одно сообщение-меню, удаляет твои ответы
и команды после обработки. Отдельными сообщениями приходят только уведомления.

## Выключение ПК

Бот заходит на ПК по SSH своим ключом (`/etc/owrt-bot/id_ed25519`, создаётся
сам). В боте: устройство → «🖥 Питание» → «🔑 Ключ и инструкция». Для Windows:

```powershell
Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
Set-Service sshd -StartupType Automatic; Start-Service sshd
```

Ключ для администратора кладётся в `C:\ProgramData\ssh\administrators_authorized_keys`.

## Файлы на роутере

| Путь | Что |
| --- | --- |
| `/usr/bin/owrt-bot` | бинарник |
| `/etc/config/owrt-bot` | настройки (UCI): токен, чаты, пороги алертов |
| `/etc/owrt-bot/config.conf` | старый INI, нужен только для переноса (`owrt-bot -migrate`) |
| `/etc/owrt-bot/wol.json` | устройства, группы, расписания |
| `/etc/owrt-bot/prefs.json` | язык |
| `/etc/owrt-bot/id_ed25519`, `known_hosts` | SSH-ключ бота и закреплённые ключи ПК |
| `/var/log/owrt-bot.log` | лог с ротацией |

`/etc/owrt-bot/` добавляется в `sysupgrade.conf` и переживает прошивку.

Ключи, которых нет в LuCI (секция `bot`, через `uci set owrt-bot.bot.<ключ>=…`):
`lan_iface` (по умолчанию `network.lan.device`), `dns_server` (`ip:порт` в
обход системного резолвера), `speedtest_url` (свои серверы через запятую).

Диагностика без Telegram:

```sh
owrt-bot -probe                    # статус всех сохранённых устройств
owrt-bot -wake 02:5e:10:aa:bb:01   # отправить magic-пакеты
owrt-bot -speedtest                # тест скорости WAN (4 потока, 8 с)
owrt-bot -check                    # проверить настройки и токен
```

## Разработка

```sh
go test ./...                 # тесты идут на любой ОС, сокеты под //go:build linux
sh scripts/build.sh v0.2.0    # бинарники всех архитектур + checksums.txt в dist/
```

Тег `v*` запускает GitHub Actions: тесты, сборка, релиз с бинарниками.

```text
cmd/owrt-bot        точка сборки: создаёт конкретные типы и связывает их
internal/telegram   клиент Bot API (keep-alive HTTP/2, пинги, токен вычищается из ошибок)
internal/bot        диспетчер: доступ, очереди по чатам, роутинг, сессии, живые экраны
internal/wol        домен WOL: модель, хранилище, присутствие, будильщик, наблюдатель, расписание, SSH
internal/netx       magic-пакет, ARP-проба, таблица соседей, ping/TCP
internal/sys        запуск команд без shell, procfs, uci, аренды, метрики, часовой пояс
internal/features/* по пакету на раздел меню; wolui — интерфейс WOL
internal/ui         спарклайны и полосы для сообщений
internal/i18n       ru/en; тест падает, если ключа нет в одном из каталогов
```

Новый раздел меню: тип с методом `Register(*bot.Router)`, строки в
`internal/i18n`, одна строка в `cmd/owrt-bot/main.go`. Главное меню, доступ и
роутинг менять не нужно. Экран, который должен обновляться сам, рисуется
через `c.Live(render, bot.LiveOptions{Every: 3 * time.Second})`.
