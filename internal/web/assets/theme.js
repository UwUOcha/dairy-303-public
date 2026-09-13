// Тема до первой отрисовки. app.js — модуль, и до его запуска успевает
// пройти загрузка графа импортов: без этого файла
// светлый фон вспыхивал бы на полсекунды даже у тех, кто выбрал тёмную.
// Отдельный файл, а не инлайн: CSP сайта не разрешает inline-скрипты.
(function () {
  var theme = "";
  try {
    var config = JSON.parse(
      document.querySelector('meta[name="app-config"]').content,
    );
    var raw = localStorage.getItem(
      (config.demo ? "demo." : "") + "mp.preferences.v1",
    );
    if (raw) theme = JSON.parse(raw).theme;
  } catch (e) {}
  if (theme !== "light" && theme !== "dark" && theme !== "night")
    theme = matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";
  document.documentElement.dataset.theme = theme;
  var meta = document.querySelector('meta[name="theme-color"]');
  if (meta)
    meta.content = theme === "night" ? "#121525" : theme === "dark" ? "#202620" : "#f6f7f4";
  var manifest = document.querySelector('link[rel="manifest"]');
  if (manifest) manifest.href = theme === "light" ? "/manifest.webmanifest" : "/manifest-" + theme + ".webmanifest";
  if (typeof window === "undefined") return;
  // Keep startup diagnostics outside the module graph: an import failure means
  // no line of app.js can execute, including its own error handling.
  var bootState = window.mpBoot = { stage: "modules", errors: [] };
  var diagnosticText, failureShown = false;
  function clean(value) {
    return String(value || "").replace(/https?:\/\/[^\s"'<>]+/g, function (value) {
      try { return new URL(value).pathname; } catch (_) { return "[URL]"; }
    }).slice(0, 1800);
  }
  function record(value) {
    bootState.errors.push(clean(value));
    bootState.errors = bootState.errors.slice(-4);
    if (diagnosticText) report();
  }
  function report() {
    if (!diagnosticText) return;
    diagnosticText.textContent = [
      "Запуск: pwa-21",
      "Этап: " + bootState.stage,
      "Сеть: " + (navigator.onLine ? "онлайн" : "офлайн"),
      "Браузер: " + navigator.userAgent,
      "SW: " + (navigator.serviceWorker && navigator.serviceWorker.controller ? navigator.serviceWorker.controller.state : "нет контроллера"),
      "Версия контроллера: " + (bootState.controllerVersion || "не отвечает на запрос версии"),
      bootState.cache || "Кеш: проверяем…",
      bootState.errors.length ? bootState.errors.join("\n") : "Ошибка не передана браузером (ожидание загрузки)",
    ].join("\n");
  }
  function identifyController() {
    var controller = navigator.serviceWorker && navigator.serviceWorker.controller;
    if (!controller || typeof MessageChannel === "undefined") return;
    var channel = new MessageChannel();
    var timer = setTimeout(function () { channel.port1.close(); }, 1500);
    channel.port1.onmessage = function (event) {
      clearTimeout(timer);
      bootState.controllerVersion = clean(event.data && event.data.cache);
      channel.port1.close();
      report();
    };
    controller.postMessage({ type: "MP_VERSION" }, [channel.port2]);
  }
  async function inspectCache() {
    try {
      var names = (await caches.keys()).filter(function (name) { return name.startsWith("mp-shell-"); });
      var results = await Promise.all(names.map(async function (name) {
        var cache = await caches.open(name), requests = await cache.keys();
        var scripts = requests.filter(function (request) { return /\.(m?js)$/.test(new URL(request.url).pathname); });
        var files = await Promise.all(scripts.map(async function (request) {
          var response = await cache.match(request, { ignoreVary: true });
          return new URL(request.url).pathname + "=" + (response ? response.status + " " + response.headers.get("Content-Type") : "нет");
        }));
        return name + ": " + requests.length + " файлов\n" + files.join("\n");
      }));
      bootState.cache = "Кеш: " + (results.join("\n") || "отсутствует");
    } catch (error) { bootState.cache = "Кеш: " + clean(error.name + ": " + error.message); }
    report();
  }
  function showFailure() {
    var boot = document.querySelector(".boot");
    if (!boot || failureShown) return;
    failureShown = true;
    var message = boot.querySelector("p");
    if (message) message.textContent = "Не удалось запустить приложение. Сохранённое расписание и настройки не удалены.";
    var retry = document.createElement("button");
    retry.textContent = "Повторить запуск";
    retry.className = "button";
    retry.onclick = function () { location.reload(); };
    var details = document.createElement("details"), summary = document.createElement("summary");
    summary.textContent = "Подробности ошибки";
    diagnosticText = document.createElement("pre");
    details.append(summary, diagnosticText);
    boot.append(retry, details);
    report();
    inspectCache();
  }
  window.addEventListener("error", function (event) {
    if (bootState.stage === "ready") return;
    var target = event.target;
    if (target && target !== window && target.tagName !== "SCRIPT") return;
    record(event.message ? event.message + " at " + clean(event.filename) + ":" + event.lineno + ":" + event.colno : "Не загружен скрипт: " + clean(target && target.src));
    showFailure();
  }, true);
  window.addEventListener("unhandledrejection", function (event) {
    if (bootState.stage === "ready") return;
    var error = event.reason;
    record(error && error.stack || error && (error.name + ": " + error.message) || error);
    showFailure();
  });
  // A complete new shell may activate even while Firefox retains an old PWA
  // client. Reload once so its HTML/CSS/modules all come from the new worker.
  // A first installation taking control of an online page needs no reload.
  if (navigator.serviceWorker) {
    var hadController = !!navigator.serviceWorker.controller, reloading = false;
    navigator.serviceWorker.addEventListener("controllerchange", function () {
      if (hadController && !reloading) {
        reloading = true;
        location.reload();
      }
      hadController = !!navigator.serviceWorker.controller;
      identifyController();
    });
    identifyController();
  }
  // Registration is independent of app imports. Its failure is useful evidence,
  // but must not prevent normal online startup or erase an existing cache.
  if ("serviceWorker" in navigator && window.isSecureContext)
    navigator.serviceWorker.register("/sw.js").catch(function (error) {
      record("Регистрация SW: " + error.name + ": " + error.message);
    });
  document.addEventListener("DOMContentLoaded", function () {
    if (bootState.errors.length) showFailure();
  });
  setTimeout(showFailure, 12000);
})();
