'use strict';
'require view';
'require form';
'require fs';
'require ui';
'require uci';
'require rpc';

// LuCI page for owrt-bot: settings in /etc/config/owrt-bot, plus status,
// token check, "allow this chat" for people who already wrote to the bot,
// and the log. Saving triggers procd's reload trigger, which restarts the bot.

var callServiceList = rpc.declare({
	object: 'service',
	method: 'list',
	params: ['name'],
	expect: { '': {} }
});

function isRunning() {
	return L.resolveDefault(callServiceList('owrt-bot'), {}).then(function(res) {
		var inst = res && res['owrt-bot'] && res['owrt-bot'].instances;
		if (!inst)
			return false;
		for (var k in inst)
			if (inst[k].running)
				return true;
		return false;
	});
}

function readJSON(path) {
	return L.resolveDefault(fs.read(path), '[]').then(function(text) {
		try { return JSON.parse(text) || []; } catch (e) { return []; }
	});
}

function tail(text, n) {
	var lines = (text || '').trim().split('\n');
	return lines.slice(Math.max(0, lines.length - n)).join('\n');
}

return view.extend({
	load: function() {
		return Promise.all([
			isRunning(),
			L.resolveDefault(fs.exec('/usr/bin/owrt-bot', ['-version']), {}),
			readJSON('/tmp/owrt-bot/pending.json'),
			L.resolveDefault(fs.read('/var/log/owrt-bot.log'), ''),
			uci.load('owrt-bot')
		]);
	},

	handleRestart: function() {
		return fs.exec('/etc/init.d/owrt-bot', ['restart']).then(function() {
			ui.addNotification(null, E('p', _('Бот перезапускается…')), 'info');
			window.setTimeout(function() { location.reload(); }, 2500);
		}).catch(function(e) {
			ui.addNotification(null, E('p', _('Не удалось перезапустить: %s').format(e.message)), 'danger');
		});
	},

	handleCheck: function(out) {
		out.textContent = _('Проверяю…');
		return fs.exec('/usr/bin/owrt-bot', ['-check']).then(function(res) {
			var ok = res.code === 0;
			out.style.color = ok ? 'green' : 'red';
			out.textContent = ok ? (res.stdout || '').trim() : (res.stderr || res.stdout || _('ошибка')).trim();
		}).catch(function(e) {
			out.style.color = 'red';
			out.textContent = e.message;
		});
	},

	// Adds the chat to the allowed list and applies, so the bot restarts
	// and starts answering that person right away.
	handleAllow: function(id) {
		var list = L.toArray(uci.get('owrt-bot', 'telegram', 'allowed_chat_id'));
		if (list.indexOf(String(id)) < 0)
			list.push(String(id));
		uci.set('owrt-bot', 'telegram', 'allowed_chat_id', list);
		return uci.save().then(function() { return ui.changes.apply(true); });
	},

	renderStatus: function(running, version) {
		var out = E('span', { 'style': 'margin-left:1em' });
		var ver = (version && version.stdout || '').trim() || '?';
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', _('Состояние')),
			E('p', {}, [
				running
					? E('span', { 'style': 'color:green;font-weight:bold' }, _('● Работает'))
					: E('span', { 'style': 'color:red;font-weight:bold' }, _('● Остановлен')),
				E('span', { 'style': 'margin-left:1em' }, _('Версия: %s').format(ver))
			]),
			E('div', {}, [
				E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'handleCheck', out) }, _('Проверить токен')),
				' ',
				E('button', { 'class': 'cbi-button cbi-button-reset', 'click': ui.createHandlerFn(this, 'handleRestart') }, _('Перезапустить')),
				out
			])
		]);
	},

	renderPending: function(pending) {
		var allowed = L.toArray(uci.get('owrt-bot', 'telegram', 'allowed_chat_id'));
		var rows = pending.filter(function(p) { return allowed.indexOf(String(p.id)) < 0; });
		var hint = E('p', { 'class': 'cbi-section-descr' },
			_('Напиши своему боту в Telegram любое сообщение и обнови страницу: чат появится здесь, его можно разрешить одной кнопкой.'));
		if (!rows.length)
			return E('div', { 'class': 'cbi-section' }, [E('h3', _('Новые чаты')), hint, E('em', _('Пока никто не писал.'))]);

		var table = E('table', { 'class': 'table' }, [
			E('tr', { 'class': 'tr table-titles' }, [
				E('th', { 'class': 'th' }, _('Кто')),
				E('th', { 'class': 'th' }, _('Chat ID')),
				E('th', { 'class': 'th' }, _('Когда')),
				E('th', { 'class': 'th' }, '')
			])
		]);
		var self = this;
		rows.forEach(function(p) {
			table.appendChild(E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td' }, (p.name || '?') + (p.username ? ' (@' + p.username + ')' : '')),
				E('td', { 'class': 'td' }, String(p.id)),
				E('td', { 'class': 'td' }, (p.at || '').replace('T', ' ').substring(0, 19)),
				E('td', { 'class': 'td' }, E('button', {
					'class': 'cbi-button cbi-button-positive',
					'click': ui.createHandlerFn(self, 'handleAllow', p.id)
				}, _('Разрешить')))
			]));
		});
		return E('div', { 'class': 'cbi-section' }, [E('h3', _('Новые чаты')), hint, table]);
	},

	renderLog: function(text) {
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', _('Лог')),
			E('pre', { 'style': 'max-height:24em;overflow:auto;white-space:pre-wrap;font-size:90%' },
				tail(text, 40) || _('(пусто)'))
		]);
	},

	render: function(data) {
		var running = data[0], version = data[1], pending = data[2], log = data[3];
		var m, s, o;

		m = new form.Map('owrt-bot', _('Telegram-бот'),
			_('Настройки owrt-bot. После «Сохранить и применить» бот перезапустится сам. Устройства, группы и расписание пробуждения настраиваются в самом боте.'));

		s = m.section(form.NamedSection, 'telegram', 'telegram', _('Telegram'));
		o = s.option(form.Value, 'bot_token', _('Токен бота'), _('Выдаёт @BotFather, вида <code>123456:ABC-DEF…</code>'));
		o.password = true;
		o.rmempty = false;
		o.validate = function(section_id, value) {
			return /^[0-9]+:[A-Za-z0-9_-]{30,}$/.test(value) ? true : _('Токен выглядит как цифры, двоеточие и 30+ символов');
		};
		o = s.option(form.DynamicList, 'allowed_chat_id', _('Разрешённые чаты'),
			_('Telegram ID людей или групп, которым бот отвечает. Остальным он отвечает «доступ запрещён».'));
		o.datatype = 'integer';

		s = m.section(form.NamedSection, 'alerts', 'alerts', _('Уведомления о проблемах'));
		o = s.option(form.Flag, 'enabled', _('Включены'));
		o.default = '1';
		o.rmempty = false;
		[['cpu_percent', _('Порог CPU, %'), '90', 'range(1,100)'],
		 ['mem_percent', _('Порог памяти, %'), '90', 'range(1,100)'],
		 ['disk_percent', _('Порог диска, %'), '90', 'range(1,100)'],
		 ['temp_celsius', _('Порог температуры, °C'), '80', 'range(30,120)'],
		 ['check_interval_sec', _('Проверять каждые, с'), '60', 'min(10)'],
		 ['wan_fail_need', _('Сбоев ping подряд до «интернет пропал»'), '3', 'range(1,30)'],
		 ['wan_ok_need', _('Успехов ping подряд до «интернет вернулся»'), '3', 'range(1,30)']
		].forEach(function(f) {
			o = s.option(form.Value, f[0], f[1]);
			o.placeholder = f[2];
			o.datatype = f[3];
			o.depends('enabled', '1');
		});
		o = s.option(form.Value, 'wan_check_host', _('Адрес для проверки интернета'));
		o.placeholder = '1.1.1.1';
		o.datatype = 'host';
		o.depends('enabled', '1');

		s = m.section(form.NamedSection, 'bot', 'bot', _('Дополнительно'));
		o = s.option(form.Value, 'lan_iface', _('LAN-интерфейс'), _('Куда отправлять пакеты пробуждения и ARP-пробы.'));
		o.placeholder = 'br-lan';
		o = s.option(form.Value, 'dns_server', _('DNS для Telegram'), _('<code>ip:порт</code>, если системный резолвер не подходит. Пусто: системный.'));
		o.placeholder = '127.0.0.1:53';
		o = s.option(form.Value, 'speedtest_url', _('Серверы теста скорости'), _('Через запятую. Пусто: Cloudflare, OVH, Selectel.'));
		o = s.option(form.Value, 'services', _('Службы на экране «Система»'),
			_('<code>Имя:процесс[:init-скрипт]</code> через запятую. Пусто: ZeroBlock, sing-box, Zapret, Tailscale, TorrServer (показываются только установленные).'));

		s = m.section(form.NamedSection, 'logging', 'logging', _('Лог'));
		o = s.option(form.ListValue, 'level', _('Подробность'));
		o.value('error', 'error');
		o.value('warn', 'warn');
		o.value('info', 'info');
		o.value('debug', 'debug');
		o.default = 'info';
		o = s.option(form.Value, 'max_size_kb', _('Размер до ротации, КБ'));
		o.placeholder = '256';
		o.datatype = 'range(16,10240)';

		var self = this;
		return m.render().then(function(mapEl) {
			return E('div', {}, [
				self.renderStatus(running, version),
				self.renderPending(pending),
				mapEl,
				self.renderLog(log)
			]);
		});
	}
});
