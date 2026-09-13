// The quick switch cycles visible themes. System mode is selected in Settings.
export function nextThemeValue(preference, effective = "light") {
 const order = ["light", "dark", "night"];
 const current = preference === "system" ? effective : preference;
 return order[(Math.max(0, order.indexOf(current)) + 1) % order.length];
}
