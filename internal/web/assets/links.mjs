import { installation } from './config.mjs';
export const makeBotLinks = (config) => [
  { name: 'Telegram', label: 'Открыть Telegram-бота', url: config.telegram_url, mark: 'TG', start: true },
  { name: 'ВКонтакте', label: 'Открыть бота ВКонтакте', url: config.vk_url, mark: 'VK' },
].filter(link => !!link.url);
export const botLinks = makeBotLinks(installation);
export const botLink = (link, group = 0) => {
  if (!link.start || !(group > 0)) return link.url;
  const url = new URL(link.url); url.searchParams.set('start', `g${group}`); return url.href;
};
