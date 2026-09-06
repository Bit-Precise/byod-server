import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "../../lib/utils";
import type { ButtonHTMLAttributes } from "react";

const variants = cva("inline-flex items-center justify-center rounded-md px-3 py-2 text-sm font-medium transition-colors disabled:pointer-events-none disabled:opacity-50", { variants: { variant: { default: "bg-slate-900 text-white hover:bg-slate-700", outline: "border border-slate-200 bg-white hover:bg-slate-50", ghost: "hover:bg-slate-100" } }, defaultVariants: { variant: "default" } });
export function Button({ className, variant, asChild = false, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof variants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "button";
  return <Comp className={cn(variants({ variant }), className)} {...props} />;
}
