import { fileURLToPath } from "node:url";

// 정적 내보내기: `NEXT_EXPORT=1 next build`로 순수 정적 디렉터리를 web/out에 생성하며, 그대로
// nginx web 루트에 넣어 실행할 수 있다. 개발(next dev)에서는 이 변수를 설정하지 않으며 /api 리버스 프록시와 핫 리로드를 유지한다.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel 데모: 사이트 전체가 mock으로 동작하며 백엔드가 없으므로 /api 리버스 프록시가 필요 없다.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // 상위 디렉터리의 lockfile이 루트 디렉터리 추론 및 리소스 경로 생성에 영향을 주지 않도록 한다.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // 로컬 네트워크 IP에서 dev 리소스(HMR)에 접근하도록 허용하며, 필요에 따라 추가/삭제한다.
  // dev 단계에서는 모든 IPv4 출처의 /_next/* 및 HMR 접근을 허용한다(로컬 네트워크 IP가 바뀌어도 영향 없음).
  // 참고: Next는 보안상 맨 "*"를 금지하므로 구간별 와일드카드를 써야 하며, "*.*.*.*"는 임의의 IPv4와 매칭된다.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // 순수 정적 내보내기: Node 런타임 없음; 이미지 최적화 안 함; 각 라우트는 <route>/index.html을 생성한다.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock 데모: 백엔드가 없으므로 /api 리버스 프록시가 필요 없다.
          images: { unoptimized: true },
        }
      : {
          // 개발: /api/*를 Go 백엔드로 리버스 프록시한다(기본 :8787, AUTOPENTEST_API로 덮어쓸 수 있음).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;
