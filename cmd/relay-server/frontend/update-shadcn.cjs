#!/usr/bin/env node

const fs = require("fs");
const path = require("path");
const { execSync } = require("child_process");

/**
 * shadcn 컴포넌트를 자동으로 업데이트하는 스크립트
 * components.json 파일의 aliases.components 경로를 읽어서
 * 해당 경로의 ui 폴더 안에 있는 모든 컴포넌트를 업데이트합니다.
 */

function main() {
  try {
    // components.json 파일 읽기
    const componentsJsonPath = path.join(process.cwd(), "components.json");

    if (!fs.existsSync(componentsJsonPath)) {
      console.error("❌ components.json 파일을 찾을 수 없습니다.");
      process.exit(1);
    }

    const componentsConfig = JSON.parse(
      fs.readFileSync(componentsJsonPath, "utf8")
    );

    // aliases.components 경로 가져오기
    const componentsPath = componentsConfig.aliases?.components;
    if (!componentsPath) {
      console.error(
        "❌ components.json에서 aliases.components 경로를 찾을 수 없습니다."
      );
      process.exit(1);
    }

    // ui 폴더 경로 구성
    const uiPath = path.join(
      process.cwd(),
      "src",
      componentsPath.replace("@/", ""),
      "ui"
    );

    if (!fs.existsSync(uiPath)) {
      console.error(`❌ UI 컴포넌트 폴더를 찾을 수 없습니다: ${uiPath}`);
      process.exit(1);
    }

    // ui 폴더의 모든 .tsx 파일 찾기
    const files = fs
      .readdirSync(uiPath)
      .filter((file) => file.endsWith(".tsx"))
      .map((file) => path.basename(file, ".tsx"));

    if (files.length === 0) {
      console.log("📁 ui 폴더에서 .tsx 파일을 찾을 수 없습니다.");
      return;
    }

    console.log(`🔍 발견된 shadcn 컴포넌트들 (${files.length}개):`);
    files.forEach((file) => console.log(`  - ${file}`));
    console.log("");

    // 각 컴포넌트 업데이트
    let successCount = 0;
    let failCount = 0;

    for (const componentName of files) {
      try {
        console.log(`🔄 업데이트 중: ${componentName}...`);

        const command = `npx shadcn@latest add -o -y ${componentName}`;
        execSync(command, {
          stdio: "pipe",
          encoding: "utf8",
          env: { ...process.env, npm_config_legacy_peer_deps: "true" },
        });

        console.log(`✅ ${componentName} 업데이트 완료`);
        successCount++;
      } catch (error) {
        console.error(`❌ ${componentName} 업데이트 실패:`, error.message);
        failCount++;
      }
    }

    console.log("");
    console.log("📊 업데이트 결과:");
    console.log(`  ✅ 성공: ${successCount}개`);
    console.log(`  ❌ 실패: ${failCount}개`);
    console.log(`  📁 총 컴포넌트: ${files.length}개`);

    if (failCount === 0) {
      console.log("🎉 모든 shadcn 컴포넌트가 성공적으로 업데이트되었습니다!");
    }
  } catch (error) {
    console.error("❌ 스크립트 실행 중 오류가 발생했습니다:", error.message);
    process.exit(1);
  }
}

// 스크립트가 직접 실행될 때만 main 함수 호출
if (require.main === module) {
  main();
}

module.exports = { main };
