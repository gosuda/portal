import * as React from "react";
import { cn } from "@/lib/utils";

export interface InputProps
  extends React.InputHTMLAttributes<HTMLInputElement> {}

const Input = React.forwardRef<HTMLInputElement, InputProps>(
  ({ className, type, ...props }, ref) => {
    return (
      <input
        type={type}
        className={cn(
          "flex w-full min-w-0 flex-1 resize-none overflow-hidden rounded-r-lg text-foreground focus:outline-0 focus:ring-2 focus:ring-primary/50 border-none bg-border h-full placeholder:text-text-muted px-4 pl-2 text-base font-normal leading-normal",
          className
        )}
        ref={ref}
        {...props}
      />
    );
  }
);
Input.displayName = "Input";

export { Input };
