import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";
import { cn } from "../../lib/utils";

const variants = cva("inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors", {
  variants: {
    variant: {
      default: "border-transparent bg-slate-900 text-white",
      secondary: "border-transparent bg-slate-100 text-slate-700",
      success: "border-transparent bg-emerald-50 text-emerald-700",
      warning: "border-transparent bg-amber-50 text-amber-700",
      destructive: "border-transparent bg-red-50 text-red-700",
      outline: "text-slate-700",
    },
  },
  defaultVariants: { variant: "default" },
});

export function Badge({ className, variant, ...props }: HTMLAttributes<HTMLDivElement> & VariantProps<typeof variants>) {
  return <div className={cn(variants({ variant }), className)} {...props} />;
}
