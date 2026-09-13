import { nextThemeValue } from "./themes.mjs";
const key = "mp.preferences.v1";
const names = {light:"Светлая",dark:"Тёмная",night:"Звёздная ночь",system:"Системная"};
const marks = {light:"☀",dark:"☾",night:"✦",system:"◐"};
const media = matchMedia("(prefers-color-scheme: dark)");
let preferences = {};
try { preferences = JSON.parse(localStorage.getItem(key) || "{}") || {}; } catch {}
let mode = names[preferences.theme] ? preferences.theme : "system";
function apply() {
 const effective = mode === "system" ? (media.matches ? "dark" : "light") : mode;
 document.documentElement.dataset.theme = effective;
 document.querySelector('meta[name="theme-color"]').content = effective === "night" ? "#121525" : effective === "dark" ? "#202620" : "#f6f7f4";
 const button = document.querySelector('#theme-toggle');
 button.textContent = marks[mode];
 button.title = `Тема: ${names[mode]}. Включить: ${names[nextThemeValue(mode,effective)]}`;
 button.setAttribute('aria-label',button.title);
 window.dispatchEvent(new Event('resize'));
}
document.querySelector('#theme-toggle').addEventListener('click',()=>{
 mode = nextThemeValue(mode,document.documentElement.dataset.theme);
 try { const fresh = JSON.parse(localStorage.getItem(key)||"{}"); localStorage.setItem(key,JSON.stringify({...fresh,theme:mode})); } catch {}
 apply();
});
media.addEventListener('change',apply);
apply();
if (window.mpBoot) window.mpBoot.stage = 'ready';
