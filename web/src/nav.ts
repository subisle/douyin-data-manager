import { createContext, useContext } from "react";

// 轻量导航：tab 写进 location.hash（#/daily?date=2026-09-22），
// 刷新不丢、可分享；跨页跳转的参数也走 hash，页面自己按需消费。

export type NavParams = Record<string, string>;

export const NAV_KEYS = [
  "home",
  "daily",
  "monthly",
  "yearly",
  "persons",
  "import",
  "cleanup",
  "export",
  "bots",
] as const;

export function parseHash(): { tab: string; params: NavParams } {
  const raw = window.location.hash.replace(/^#\/?/, "");
  const [path, qs] = raw.split("?");
  const tab = (NAV_KEYS as readonly string[]).includes(path) ? path : "home";
  const params: NavParams = {};
  for (const [k, v] of new URLSearchParams(qs ?? "")) params[k] = v;
  return { tab, params };
}

export function buildHash(tab: string, params?: NavParams): string {
  const qs = params ? new URLSearchParams(params).toString() : "";
  return "#/" + tab + (qs ? "?" + qs : "");
}

export const NavContext = createContext<{
  tab: string;
  params: NavParams;
  navigate: (tab: string, params?: NavParams) => void;
}>({ tab: "home", params: {}, navigate: () => {} });

export function useNav() {
  return useContext(NavContext);
}
