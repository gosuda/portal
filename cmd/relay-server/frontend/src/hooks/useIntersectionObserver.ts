import { useEffect, useRef, useState } from "react";

interface UseIntersectionObserverProps {
  threshold?: number;
  root?: Element | null;
  rootMargin?: string;
  enabled?: boolean;
  onIntersect?: () => void;
}

export function useIntersectionObserver({
  threshold = 1.0,
  root = null,
  rootMargin = "0px",
  enabled = true,
  onIntersect,
}: UseIntersectionObserverProps) {
  const elementRef = useRef<HTMLDivElement | null>(null);
  const [isVisible, setIsVisible] = useState(false);

  useEffect(() => {
    const element = elementRef.current;
    if (!element || !enabled) return;

    const observer = new IntersectionObserver(
      (entries) => {
        const [entry] = entries;
        const isIntersecting = entry.isIntersecting;
        setIsVisible(isIntersecting);
        
        if (isIntersecting && onIntersect) {
          onIntersect();
        }
      },
      {
        threshold,
        root,
        rootMargin,
      }
    );

    observer.observe(element);

    return () => {
      observer.disconnect();
    };
  }, [threshold, root, rootMargin, enabled, onIntersect]);

  return { elementRef, isVisible };
}
