// Run with: node --test tests/login-ui.test.mjs
import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";

// Страница входа живёт до авторизации и получает из всего приложения только
// config.mjs, поэтому проверяем её отдельно от остального сайта: подменяем
// импорт настройками установки, а браузер — минимальным DOM.
const source = (await readFile("internal/web/assets/auth.js", "utf8")).replace(/^import .*\n/, "");

async function loginPage({ challenge = "c".repeat(43), clipboard = true } = {}) {
  const nodes = new Map();
  const copied = [];
  const node = () => ({
    hidden: false, textContent: "", value: "", href: "", dataset: {}, children: [],
    handlers: {},
    addEventListener(name, handler) { this.handlers[name] = handler; },
    replaceChildren(...items) { this.children = items; },
    append() {}, focus() {},
  });
  const find = (selector) => {
    if (!nodes.has(selector)) nodes.set(selector, node());
    return nodes.get(selector);
  };
  const providers = { tg: node(), vk: node() };
  providers.tg.dataset.provider = "tg";
  providers.vk.dataset.provider = "vk";
  const storage = () => ({ getItem: () => null, setItem() {}, removeItem() {} });
  const context = vm.createContext({
    installation: { telegram_url: "https://t.me/example_bot", vk_url: "https://vk.me/example_bot" },
    document: {
      querySelector: find,
      querySelectorAll: () => [providers.tg, providers.vk],
      createElement: node,
    },
    location: { pathname: "/login", search: "", replace() {} },
    // Буфер обмена — единственный способ отдать команду входа человеку,
    // которого ВКонтакте оставляет в открытом диалоге ни с чем.
    navigator: { clipboard: { writeText: async (text) => {
      if (!clipboard) throw new Error("буфер обмена недоступен");
      copied.push(text);
    } } },
    localStorage: storage(), sessionStorage: storage(),
    fetch: async (path) => path === "/auth/start"
      ? { ok: true, json: async () => ({ challenge }) }
      // Сохранённого входа нет: без 401 страница ушла бы на расписание.
      : { ok: false, status: 401, json: async () => ({ error: "Нужно войти" }) },
    URL, URLSearchParams, AbortSignal, console,
    setInterval: () => 0,
  });
  await vm.runInContext(`(async () => {\n${source}\n})()`, context);
  return {
    challenge, copied, node: find,
    start: (platform) => providers[platform].handlers.click(),
    openBot: () => find("#bot-link").handlers.click(),
  };
}

test("вход через Telegram открывает бота ссылкой с командой", async () => {
  const page = await loginPage();
  await page.start("tg");
  assert.equal(page.node("#bot-link").href, `https://t.me/example_bot?start=web_${page.challenge}`);
  assert.equal(page.node("#steps").children.length, 2);
  await page.openBot();
  assert.deepEqual(page.copied, [], "телеграм отправляет команду сам, буфер трогать незачем");
});

// Ссылка ВКонтакте только открывает диалог: метку «?ref=» бот получит лишь с
// первым сообщением нового диалога, а у постоянного пользователя бота его нет.
// Поэтому команду кладём в буфер обмена, и шагов на экране становится три.
test("вход через ВКонтакте копирует команду и показывает свои шаги", async () => {
  const page = await loginPage();
  await page.start("vk");
  assert.equal(page.node("#bot-link").href, `https://vk.me/example_bot?ref=web_${page.challenge}`);
  const steps = page.node("#steps").children.map((item) => item.textContent);
  assert.equal(steps.length, 3);
  assert.match(steps[1], /[Вв]ставь/);
  assert.match(page.node("#manual-command").textContent, new RegExp(`^/login ${page.challenge}$`));
  await page.openBot();
  assert.deepEqual(page.copied, [`/login ${page.challenge}`]);
  assert.match(page.node("#manual-hint").textContent, /скопирована/i);
});

test("отказ буфера обмена не прячет команду и говорит об этом", async () => {
  const page = await loginPage({ clipboard: false });
  await page.start("vk");
  await page.openBot();
  assert.deepEqual(page.copied, []);
  assert.match(page.node("#status").textContent, /[Нн]е удалось скопировать/);
  assert.match(page.node("#manual-command").textContent, /^\/login /);
});
