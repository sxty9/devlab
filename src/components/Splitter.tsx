import { useRef, useState } from 'react';

/**
 * A vertical drag handle between two columns. Emits horizontal deltas (px) as the user drags;
 * the parent owns and clamps the width. Arrow keys nudge for keyboard accessibility.
 */
export function Splitter({ onResize, ariaLabel = 'Resize panel' }: { onResize: (deltaX: number) => void; ariaLabel?: string }) {
  const [dragging, setDragging] = useState(false);
  const lastX = useRef(0);

  const onPointerDown = (e: React.PointerEvent) => {
    e.preventDefault();
    lastX.current = e.clientX;
    setDragging(true);
    document.body.classList.add('dl-no-select');
    document.body.style.cursor = 'col-resize';

    const move = (ev: PointerEvent) => {
      const dx = ev.clientX - lastX.current;
      lastX.current = ev.clientX;
      if (dx !== 0) onResize(dx);
    };
    const up = () => {
      setDragging(false);
      document.body.classList.remove('dl-no-select');
      document.body.style.cursor = '';
      window.removeEventListener('pointermove', move);
      window.removeEventListener('pointerup', up);
    };
    window.addEventListener('pointermove', move);
    window.addEventListener('pointerup', up);
  };

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowLeft') {
      e.preventDefault();
      onResize(-16);
    } else if (e.key === 'ArrowRight') {
      e.preventDefault();
      onResize(16);
    }
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={ariaLabel}
      tabIndex={0}
      data-dragging={dragging}
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      className="dl-splitter focus-visible:outline-none"
    />
  );
}
