// Renders screens.json (made by tools/screens on a router) as Telegram-like
// dark-theme chat cards and screenshots them; optionally screenshots the
// real LuCI page too.
//
//   cd tools/screens && npm install
//   node shoot.mjs screens.json ../../docs/img
//   LUCI_URL=http://192.168.1.1 LUCI_USER=root LUCI_PASS=... node shoot.mjs screens.json ../../docs/img
//
// The text and buttons are the bot's real output; only the chrome around
// them is drawn here.
import fs from 'node:fs';
import path from 'node:path';
import puppeteer from 'puppeteer-core';

const [, , input = 'screens.json', outDir = '../../docs/img'] = process.argv;
const browserPath = process.env.BROWSER ||
	'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
const shots = JSON.parse(fs.readFileSync(input, 'utf8'));
fs.mkdirSync(outDir, { recursive: true });

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

const css = `
body { margin: 0; background: #0e1621; font: 15px/1.35 "Segoe UI", system-ui, sans-serif; color: #f5f5f5; }
.chat { width: 460px; padding: 0 0 14px; background: #0e1621; }
.head { display: flex; align-items: center; gap: 10px; padding: 10px 14px; background: #17212b; margin-bottom: 12px; }
.ava { width: 38px; height: 38px; border-radius: 50%; background: linear-gradient(135deg,#5eb5f7,#3a7bd5);
  display: flex; align-items: center; justify-content: center; font-size: 20px; }
.name { font-weight: 600; } .sub { font-size: 13px; color: #6d7f8f; }
.row { display: flex; padding: 3px 12px; }
.me { justify-content: flex-end; }
.bubble { max-width: 400px; padding: 7px 11px 8px; border-radius: 12px; white-space: pre-wrap; word-wrap: break-word; }
.bot .bubble { background: #182533; border-bottom-left-radius: 4px; }
.me .bubble { background: #2b5278; border-bottom-right-radius: 4px; }
code, pre { font-family: Consolas, "Cascadia Mono", monospace; font-size: 13.5px; color: #8ac7ff; }
pre { margin: 4px 0 0; white-space: pre-wrap; }
i { color: #8d9eb0; }
.kb { padding: 4px 12px 0 12px; width: 412px; }
.kbrow { display: flex; gap: 4px; margin-top: 4px; }
.btn { flex: 1; text-align: center; padding: 8px 6px; border-radius: 8px; background: rgba(65, 96, 128, .55);
  font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
`;

function card(s) {
	const kb = (s.keyboard?.inline_keyboard || []).map((row) =>
		`<div class="kbrow">${row.map((b) => `<div class="btn">${esc(b.text)}</div>`).join('')}</div>`).join('');
	const mine = s.mine ? `<div class="row me"><div class="bubble">${esc(s.mine)}</div></div>` : '';
	// The bot sends Telegram HTML (<b>, <i>, <code>, <pre>): render as is.
	return `<!doctype html><meta charset="utf-8"><style>${css}</style>
<div class="chat" id="shot">
  <div class="head"><div class="ava">🤖</div><div><div class="name">Router Bot</div><div class="sub">бот</div></div></div>
  ${mine}
  <div class="row bot"><div class="bubble">${s.text}</div></div>
  <div class="kb">${kb}</div>
</div>`;
}

const browser = await puppeteer.launch({ executablePath: browserPath, headless: true });
const page = await browser.newPage();
await page.setViewport({ width: 520, height: 900, deviceScaleFactor: 2 });

for (const s of shots) {
	await page.setContent(card(s), { waitUntil: 'load' });
	const el = await page.$('#shot');
	const file = path.join(outDir, `tg-${s.name}.png`);
	await el.screenshot({ path: file });
	console.log(file);
}

if (process.env.LUCI_URL && process.env.LUCI_USER) {
	const url = new URL('/cgi-bin/luci/admin/services/owrt-bot', process.env.LUCI_URL);
	await page.setViewport({ width: 1100, height: 900, deviceScaleFactor: 1.5 });
	await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }]);
	await page.goto(url.href, { waitUntil: 'networkidle0' });
	if (await page.$('input[name=luci_password]')) {
		await page.evaluate((u, p) => {
			const user = document.querySelector('input[name=luci_username]');
			if (user) user.value = u;
			document.querySelector('input[name=luci_password]').value = p;
			document.querySelector('input[name=luci_password]').form.submit();
		}, process.env.LUCI_USER, process.env.LUCI_PASS || '');
		await page.waitForNavigation({ waitUntil: 'networkidle0' });
	}
	try {
		await page.waitForSelector('.cbi-map', { timeout: 15000 });
	} catch (e) {
		await page.screenshot({ path: path.join(outDir, 'luci-debug.png') });
		console.error('LuCI page did not load:', page.url(), await page.title());
		throw e;
	}
	// Never publish real ids: swap them for examples before the shot.
	await page.evaluate(() => {
		document.querySelectorAll('input').forEach((i) => {
			if (/^-?\d{5,}$/.test(i.value)) i.value = '123456789';
		});
		document.querySelectorAll('.cbi-dynlist .item, .cbi-dynlist span').forEach((e) => {
			if (/^-?\d{5,}$/.test(e.textContent.trim())) e.textContent = '123456789';
		});
		// The services picker would show this router's packages: show stock ones.
		const stock = ['dnsmasq', 'dropbear', 'odhcpd', 'uhttpd'];
		document.querySelectorAll('.cbi-map .cbi-dropdown > ul > li[selected]').forEach((li, i) => {
			if (i < stock.length) li.textContent = stock[i];
			else li.remove();
		});
		document.querySelectorAll('details').forEach((d) => d.remove());
		document.querySelectorAll('.alert-message, #modal_overlay').forEach((d) => d.remove());
	});
	await page.setViewport({ width: 1100, height: 2000, deviceScaleFactor: 1.5 });
	// Crop from the top of the content to the Save & Apply bar.
	const clip = await page.evaluate(() => {
		const top = (document.querySelector('#maincontent') || document.body).getBoundingClientRect();
		const end = document.querySelector('.cbi-page-actions')?.getBoundingClientRect();
		return { x: top.left, y: top.top + window.scrollY, width: top.width, height: (end ? end.bottom : top.bottom) - top.top + 16 };
	});
	const file = path.join(outDir, 'luci.png');
	await page.screenshot({ path: file, clip });
	console.log(file);
}

await browser.close();
