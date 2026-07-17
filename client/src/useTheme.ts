import { useState } from "react";

type Theme = "light" | "dark";

// The inline script in index.html has already set data-theme before React
// mounts, so the initial value is read from the DOM, not recomputed.
function currentTheme(): Theme {
  return document.documentElement.getAttribute("data-theme") === "dark"
    ? "dark"
    : "light";
}

/**
 * useTheme reads the active theme and toggles it, persisting the explicit choice
 * to localStorage so it wins over the system preference on the next load.
 */
export function useTheme() {
  const [theme, setTheme] = useState<Theme>(currentTheme);

  function toggle() {
    const next: Theme = theme === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    localStorage.setItem("theme", next);
    setTheme(next);
  }

  return { theme, toggle };
}
