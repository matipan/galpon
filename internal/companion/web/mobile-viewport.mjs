const baseFontPixels = 18;
const baseTouchPixels = 44;

export function desktopMobileScale({ innerWidth, screenWidth, screenHeight, coarsePointer }) {
  const narrowDevice = Math.min(Number(screenWidth) || 0, Number(screenHeight) || 0) <= 600;
  const width = Number(screenWidth) || 0;
  const layoutWidth = Number(innerWidth) || 0;
  if (!coarsePointer || !narrowDevice || width <= 0 || layoutWidth < 700) return 1;
  const scale = layoutWidth / width;
  return scale > 1.1 ? Math.min(scale, 3) : 1;
}

export function applyMobileViewportCompensation(view = window) {
  const root = view.document.documentElement;
  const scale = desktopMobileScale({
    innerWidth: view.innerWidth,
    screenWidth: view.screen.width,
    screenHeight: view.screen.height,
    coarsePointer: view.matchMedia("(pointer: coarse)").matches,
  });
  if (scale === 1) {
    delete root.dataset.mobileDesktop;
    root.style.removeProperty("font-size");
    root.style.removeProperty("--touch");
    return scale;
  }
  root.dataset.mobileDesktop = "true";
  root.style.fontSize = `${baseFontPixels * scale}px`;
  root.style.setProperty("--touch", `${baseTouchPixels * scale}px`);
  return scale;
}
