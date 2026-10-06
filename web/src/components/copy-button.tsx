"use client";

import * as React from "react";

import { CheckIcon, CopyIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { cn, copyText } from "@/lib/utils";

type CopyButtonProps = {
  // 복사할 텍스트; 비어 있으면 버튼이 비활성화된다.
  text: string | null | undefined;
  // 복사 성공 후 toast 문구, 기본값은 「복사됨」.
  successMessage?: string;
  label?: React.ReactNode;
  size?: React.ComponentProps<typeof Button>["size"];
  variant?: React.ComponentProps<typeof Button>["variant"];
  className?: string;
};

// CopyButton은 통일된 「클립보드에 복사」 버튼: 성공/실패 피드백을 내장하고, HTTP 비보안 컨텍스트에서는
// 자동으로 폴백한다(copyText 참고).
export function CopyButton({
  text,
  successMessage = "복사됨",
  label = "복사",
  size = "sm",
  variant = "outline",
  className,
}: CopyButtonProps) {
  const [copied, setCopied] = React.useState(false);
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  React.useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  async function handleCopy() {
    if (!text) return;
    const ok = await copyText(text);
    if (ok) {
      setCopied(true);
      toast.success(successMessage);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error("복사에 실패했습니다. 텍스트를 직접 선택하여 복사하세요");
    }
  }

  return (
    <Button
      type="button"
      size={size}
      variant={variant}
      className={cn(className)}
      disabled={!text}
      onClick={handleCopy}
    >
      {copied ? <CheckIcon /> : <CopyIcon />}
      {label}
    </Button>
  );
}
