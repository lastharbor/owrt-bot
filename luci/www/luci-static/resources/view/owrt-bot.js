'use strict';
'require view';
'require form';
'require fs';
'require ui';
'require uci';
'require rpc';

// LuCI page for owrt-bot. Settings live in /etc/config/owrt-bot; saving
// fires procd's reload trigger, which restarts the bot.

var callServices = rpc.declare({
	object: 'service',
	method: 'list',
	expect: { '': {} }
});

function running(services) {
	var inst = services['owrt-bot'] && services['owrt-bot'].instances;
	for (var k in inst || {})
		if (inst[k].running)
			return true;
	return false;
}

function readJSON(path) {
	return L.resolveDefault(fs.read(path), '[]').then(function(text) {
		try { return JSON.parse(text) || []; } catch (e) { return []; }
	});
}

return view.extend({
	load: function() {
		return Promise.all([
			L.resolveDefault(callServices(), {}),
			L.resolveDefault(fs.exec('/usr/bin/owrt-bot', ['-version']), {}),
			readJSON('/tmp/owrt-bot/pending.json'),
			L.resolveDefault(fs.read('/var/log/owrt-bot.log'), ''),
			uci.load('owrt-bot')
		]);
	},

	handleRestart: function() {
		return fs.exec('/etc/init.d/owrt-bot', ['restart']).then(function() {
			window.setTimeout(function() { location.reload(); }, 2500);
		});
	},

	handleCheck: function(out) {
		out.textContent = _('Проверяю…');
		return fs.exec('/usr/bin/owrt-bot', ['-check']).then(function(res) {
			out.style.color = res.code === 0 ? 'green' : 'red';
			out.textContent = ((res.code === 0 ? res.stdout : (res.stderr || res.stdout)) || '').trim();
		});
	},

	// Allows the chat and applies at once, so the bot answers right away.
	handleAllow: function(id) {
		var list = L.toArray(uci.get('owrt-bot', 'telegram', 'allowed_chat_id'));
		if (list.indexOf(String(id)) < 0)
			list.push(String(id));
		uci.set('owrt-bot', 'telegram', 'allowed_chat_id', list);
		return uci.save().then(function() { return ui.changes.apply(true); });
	},

	renderStatus: function(isRunning, version) {
		var out = E('span', { 'style': 'margin-left:1em' });
		return E('div', { 'class': 'cbi-section' }, [
			E('p', {}, [
				isRunning
					? E('strong', { 'style': 'color:green' }, _('● Работает'))
					: E('strong', { 'style': 'color:red' }, _('● Остановлен')),
				E('span', { 'style': 'margin-left:1em;opacity:.7' }, ((version && version.stdout) || '').trim())
			]),
			E('button', { 'class': 'cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'handleCheck', out) }, _('Проверить токен')),
			' ',
			E('button', { 'class': 'cbi-button cbi-button-reset', 'click': ui.createHandlerFn(this, 'handleRestart') }, _('Перезапустить')),
			out
		]);
	},

	renderPending: function(pending) {
		var allowed = L.toArray(uci.get('owrt-bot', 'telegram', 'allowed_chat_id'));
		var rows = pending.filter(function(p) { return allowed.indexOf(String(p.id)) < 0; });
		if (!rows.length)
			return E('div');
		var self = this;
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', _('Кто писал боту')),
			E('div', {}, rows.map(function(p) {
				return E('p', {}, [
					E('button', {
						'class': 'cbi-button cbi-button-positive',
						'click': ui.createHandlerFn(self, 'handleAllow', p.id)
					}, _('Разрешить')),
					' ',
					E('strong', p.name || '?'),
					p.username ? ' @' + p.username : '',
					E('span', { 'style': 'opacity:.6' }, ' · ' + p.id)
				]);
			}))
		]);
	},

	render: function(data) {
		var services = data[0], version = data[1], pending = data[2], log = data[3];
		var m, s, o;

		m = new form.Map('owrt-bot', _('Telegram-бот'));

		s = m.section(form.NamedSection, 'telegram', 'telegram');
		o = s.option(form.Value, 'bot_token', _('Токен бота'), _('Выдаёт @BotFather'));
		o.password = true;
		o.rmempty = false;
		o.validate = function(id, v) {
			return /^[0-9]+:[A-Za-z0-9_-]{30,}$/.test(v) || _('Неверный формат токена');
		};
		o = s.option(form.DynamicList, 'allowed_chat_id', _('Кому отвечать'),
			_('Напиши боту, и ты появишься выше в «Кто писал боту»'));
		o.datatype = 'integer';

		s = m.section(form.NamedSection, 'alerts', 'alerts', _('Уведомления'));
		o = s.option(form.Flag, 'enabled', _('Сообщать о проблемах роутера'));
		o.default = '1';
		o.rmempty = false;
		[['cpu_percent', _('Загрузка CPU, %'), '90', 'range(1,100)'],
		 ['mem_percent', _('Память, %'), '90', 'range(1,100)'],
		 ['disk_percent', _('Диск, %'), '90', 'range(1,100)'],
		 ['temp_celsius', _('Температура, °C'), '80', 'range(30,120)']
		].forEach(function(f) {
			o = s.option(form.Value, f[0], f[1]);
			o.placeholder = f[2];
			o.datatype = f[3];
			o.depends('enabled', '1');
		});

		s = m.section(form.NamedSection, 'bot', 'bot', _('Службы'));
		o = s.option(form.MultiValue, 'service', _('Показывать в боте'),
			_('Состояние и кнопка перезапуска на экране «Система»'));
		// Only daemons: one-shot scripts (firewall, sysctl, …) have no
		// instances and would always look stopped in the bot.
		Object.keys(services).sort().forEach(function(name) {
			var inst = services[name].instances;
			if (name !== 'owrt-bot' && inst && Object.keys(inst).length)
				o.value(name);
		});
		o.display_size = 8;

		var self = this;
		return m.render().then(function(mapEl) {
			var tail = (log || '').trim().split('\n').slice(-30).join('\n');
			return E('div', {}, [
				self.renderStatus(running(services), version),
				self.renderPending(pending),
				mapEl,
				E('details', { 'class': 'cbi-section' }, [
					E('summary', _('Лог')),
					E('pre', { 'style': 'max-height:24em;overflow:auto;white-space:pre-wrap;font-size:90%' }, tail || '—')
				])
			]);
		});
	}
});
