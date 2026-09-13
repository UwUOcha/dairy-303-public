// Platform integration stays separate from the schedule and device preferences.
// initDataUnsafe is never used for authentication or to access a bot profile.
let telegram, installPrompt;
window.addEventListener("beforeinstallprompt", (event) => {
  event.preventDefault();
  installPrompt = event;
});
export async function install() {
  if (!installPrompt) return false;
  const prompt = installPrompt;
  installPrompt = null;
  await prompt.prompt();
  return (await prompt.userChoice).outcome === "accepted";
}
export async function initPlatform(onBack, onTheme) {
  if (/(?:^|[?&#])tgWebAppData=/.test(location.hash + location.search)) {
    try {
      await new Promise((resolve, reject) => {
        const timer = setTimeout(
          () => reject(new Error("Telegram SDK timeout")),
          5000,
        );
        const s = document.createElement("script");
        s.src = "https://telegram.org/js/telegram-web-app.js";
        s.onload = () => {
          clearTimeout(timer);
          resolve();
        };
        s.onerror = () => {
          clearTimeout(timer);
          reject(new Error("Telegram SDK unavailable"));
        };
        document.head.append(s);
      });
      telegram = window.Telegram?.WebApp;
      if (!telegram?.initData) throw new Error("Not a Telegram launch");
      const safe = () => {
        const top =
          (telegram.safeAreaInset?.top || 0) +
          (telegram.contentSafeAreaInset?.top || 0);
        const bottom =
          (telegram.safeAreaInset?.bottom || 0) +
          (telegram.contentSafeAreaInset?.bottom || 0);
        document.documentElement.style.setProperty("--tg-top", `${top}px`);
        document.documentElement.style.setProperty(
          "--tg-bottom",
          `${bottom}px`,
        );
      };
      safe();
      telegram.onEvent("safeAreaChanged", safe);
      telegram.onEvent("contentSafeAreaChanged", safe);
      telegram.onEvent("themeChanged", () => onTheme(telegram.colorScheme));
      telegram.BackButton.onClick(onBack);
      onTheme(telegram.colorScheme);
      telegram.ready();
      telegram.expand();
    } catch {
      /* The normal website remains usable if the optional SDK fails. */
    }
  }
}
export function backButton(visible) {
  if (telegram?.initData) {
    if (visible) telegram.BackButton.show();
    else telegram.BackButton.hide();
  }
}
export async function share(url, title) {
  if (navigator.share) {
    try {
      await navigator.share({ title, url });
      return "Поделились ссылкой";
    } catch (e) {
      if (e.name === "AbortError") return "";
    }
  }
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(url);
    return "Ссылка скопирована";
  }
  return null;
}
