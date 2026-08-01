import boundaries from "eslint-plugin-boundaries";
import tseslint from "typescript-eslint";

export const architectureOwners = Object.freeze({
  API: "api",
  APP_FACADE: "app-facade",
  FEATURE: "feature",
  I18N: "i18n",
  NATIVE_CONFIG: "native-config",
  NATIVE_PACKAGE: "native-package",
  UI_KIT: "ui-kit",
  SHARED: "shared",
  SHELL: "shell",
  TEST_SUPPORT: "test-support",
  TOOLING: "tooling",
  UI: "ui",
  VENDOR: "vendor",
});

export const architectureElements = Object.freeze([
  Object.freeze({
    type: architectureOwners.SHELL,
    pattern: "src/app",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.SHELL,
    pattern: "src/dev-showcase",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.APP_FACADE,
    pattern: "src/app-facade",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.FEATURE,
    pattern: "src/features/*",
    capture: ["name"],
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.SHARED,
    pattern: "src/shared/*",
    capture: ["name"],
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.UI,
    pattern: "src/ui",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.API,
    pattern: "src/api",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.TEST_SUPPORT,
    pattern: "src/test-support",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.NATIVE_PACKAGE,
    pattern: "packages/native-bridge",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.UI_KIT,
    pattern: "packages/ui-kit",
    capture: ["name"],
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.I18N,
    pattern: "src/i18n",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.VENDOR,
    pattern: "src/vendor",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.NATIVE_CONFIG,
    pattern: "src-tauri",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.TOOLING,
    pattern: "tooling",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.TOOLING,
    pattern: "test",
    partialMatch: false,
  }),
  Object.freeze({
    type: architectureOwners.TOOLING,
    pattern: "src/types",
    partialMatch: false,
  }),
]);

export const architectureEntrypoints = Object.freeze({
  API_COMPOSITION: "composition/index.ts",
  INDEX: "index.ts",
  NATIVE_PACKAGE: "src/index.ts",
  UI_KIT: "src/index.ts",
  VENDOR_ELK_API: "elkjs-types.ts",
  VENDOR_ELK_BUNDLED: "elkjs-bundled-types.ts",
  VENDOR_XYFLOW: "xyflow-react-types.ts",
});

export const architectureFileCategories = Object.freeze({
  DECLARATION: "declaration",
  TEST: "test",
});

export const architectureFiles = Object.freeze([
  Object.freeze({
    category: architectureFileCategories.TEST,
    pattern: "**/*.test.{ts,tsx}",
  }),
  Object.freeze({
    category: architectureFileCategories.TEST,
    pattern: "test/**/*.{ts,tsx}",
  }),
  Object.freeze({
    category: architectureFileCategories.DECLARATION,
    pattern: "**/*.d.ts",
  }),
]);

export const architectureDependencyNodes = Object.freeze(["import", "export", "dynamic-import", "require"]);

export const architectureAdditionalDependencyNodes = Object.freeze(
  ["mock", "doMock", "unmock", "doUnmock", "importActual", "importMock"].map((method) =>
    Object.freeze({
      selector: `CallExpression[callee.object.name=vi][callee.property.name=${method}] > Literal.arguments:first-child`,
      kind: "value",
      name: `vi.${method}`,
    }),
  ),
);

const shellDependencyPolicies = Object.freeze([
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.SHELL,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.APP_FACADE,
    source: "@/app-facade",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.FEATURE,
    source: "@/features/*",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.SHARED,
    source: "@/shared/*",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.UI,
    source: "@/ui",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.API,
    source: "@/api",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.API,
    source: "@/api/composition",
    targetFile: architectureEntrypoints.API_COMPOSITION,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.I18N,
    source: "@/i18n",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.NATIVE_PACKAGE,
    source: "@app/native-bridge",
    targetFile: architectureEntrypoints.NATIVE_PACKAGE,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.VENDOR,
    source: "@xyflow/react",
    targetFile: architectureEntrypoints.VENDOR_XYFLOW,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk-api",
    targetFile: architectureEntrypoints.VENDOR_ELK_API,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHELL,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk.bundled.js",
    targetFile: architectureEntrypoints.VENDOR_ELK_BUNDLED,
  }),
]);

const appFacadeDependencyPolicies = Object.freeze([
  allowOwnerDependency({
    from: architectureOwners.APP_FACADE,
    to: architectureOwners.API,
    source: "@/api",
  }),
  allowOwnerDependency({
    from: architectureOwners.APP_FACADE,
    to: architectureOwners.UI,
    source: "@/ui",
  }),
  allowOwnerDependency({
    from: architectureOwners.APP_FACADE,
    to: architectureOwners.NATIVE_PACKAGE,
    source: "@app/native-bridge",
    targetFile: architectureEntrypoints.NATIVE_PACKAGE,
  }),
]);

const featureDependencyPolicies = Object.freeze([
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.APP_FACADE,
    source: "@/app-facade",
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.SHARED,
    source: "@/shared/*",
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.UI,
    source: "@/ui",
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.API,
    source: "@/api",
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.I18N,
    source: "@/i18n",
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.VENDOR,
    source: "@xyflow/react",
    targetFile: architectureEntrypoints.VENDOR_XYFLOW,
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk-api",
    targetFile: architectureEntrypoints.VENDOR_ELK_API,
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk.bundled.js",
    targetFile: architectureEntrypoints.VENDOR_ELK_BUNDLED,
  }),
  allowOwnerDependency({
    from: architectureOwners.FEATURE,
    to: architectureOwners.UI_KIT,
    source: "@app/ui-kit",
    targetFile: architectureEntrypoints.UI_KIT,
  }),
]);

const sharedDependencyPolicies = Object.freeze([
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.APP_FACADE,
    source: "@/app-facade",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.API,
    source: "@/api",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.UI,
    source: "@/ui",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.SHARED,
    source: "@/shared/*",
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.VENDOR,
    source: "@xyflow/react",
    targetFile: architectureEntrypoints.VENDOR_XYFLOW,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk-api",
    targetFile: architectureEntrypoints.VENDOR_ELK_API,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk.bundled.js",
    targetFile: architectureEntrypoints.VENDOR_ELK_BUNDLED,
  }),
  allowOwnerDependency({
    from: architectureOwners.SHARED,
    to: architectureOwners.UI_KIT,
    source: "@app/ui-kit",
    targetFile: architectureEntrypoints.UI_KIT,
  }),
]);

const testSupportDependencyPolicies = Object.freeze([
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.APP_FACADE,
    source: "@/app-facade",
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.FEATURE,
    source: "@/features/*",
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.SHARED,
    source: "@/shared/*",
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.UI,
    source: "@/ui",
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.API,
    source: "@/api",
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.API,
    source: "@/api/composition",
    targetFile: architectureEntrypoints.API_COMPOSITION,
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.I18N,
    source: "@/i18n",
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.NATIVE_PACKAGE,
    source: "@app/native-bridge",
    targetFile: architectureEntrypoints.NATIVE_PACKAGE,
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.VENDOR,
    source: "@xyflow/react",
    targetFile: architectureEntrypoints.VENDOR_XYFLOW,
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk-api",
    targetFile: architectureEntrypoints.VENDOR_ELK_API,
  }),
  allowOwnerDependency({
    from: architectureOwners.TEST_SUPPORT,
    to: architectureOwners.VENDOR,
    source: "elkjs/lib/elk.bundled.js",
    targetFile: architectureEntrypoints.VENDOR_ELK_BUNDLED,
  }),
]);

const toolingDependencyPolicies = Object.freeze([
  allowOwnerDependency({
    from: architectureOwners.TOOLING,
    to: architectureOwners.TOOLING,
    source: "@/types",
  }),
]);

const testDependencyPolicies = Object.freeze([
  Object.freeze({
    from: {
      file: {
        categories: architectureFileCategories.TEST,
      },
    },
    allow: {
      to: {
        element: {
          types: architectureOwners.TEST_SUPPORT,
          fileInternalPath: "*/index.ts",
        },
      },
      dependency: {
        source: "@/test-support/*",
      },
    },
  }),
  Object.freeze({
    from: {
      file: {
        categories: architectureFileCategories.TEST,
      },
    },
    allow: {
      to: {
        element: {
          types: architectureOwners.NATIVE_CONFIG,
          fileInternalPath: ["tauri.conf.json", "capabilities/default.json"],
        },
      },
      dependency: {
        source: ["../src-tauri/**", "../../src-tauri/**"],
      },
    },
  }),
]);

export const architectureDependencyOptions = Object.freeze({
  default: "disallow",
  checkUnknownLocals: true,
  policies: Object.freeze([
    ...shellDependencyPolicies,
    ...appFacadeDependencyPolicies,
    ...featureDependencyPolicies,
    ...sharedDependencyPolicies,
    ...testSupportDependencyPolicies,
    ...toolingDependencyPolicies,
    ...testDependencyPolicies,
  ]),
});

export function createArchitecturePolicy({ rootPath, parserProjects }) {
  return [
    {
      files: ["**/*.{ts,tsx}"],
      languageOptions: {
        parser: tseslint.parser,
        parserOptions: {
          project: parserProjects,
          tsconfigRootDir: rootPath,
        },
      },
      plugins: {
        boundaries,
      },
      settings: {
        "boundaries/elements": architectureElements,
        "boundaries/elements-single-type": true,
        "boundaries/files": architectureFiles,
        "boundaries/root-path": rootPath,
        "boundaries/dependency-nodes": architectureDependencyNodes,
        "boundaries/additional-dependency-nodes": architectureAdditionalDependencyNodes,
        "import/resolver": {
          typescript: {
            extensions: [".css", ".js", ".jsx", ".json", ".ts", ".tsx"],
            project: parserProjects,
          },
        },
      },
      rules: {
        "boundaries/dependencies": ["error", architectureDependencyOptions],
        "boundaries/no-unknown-dependencies": ["error", { require: "element" }],
        "boundaries/no-unknown-files": "error",
      },
    },
    {
      files: ["**/*.{ts,tsx}"],
      ignores: ["packages/native-bridge/**"],
      rules: {
        "no-restricted-imports": [
          "error",
          {
            paths: [
              {
                name: "@tauri-apps/api",
                message: "Import Tauri APIs only inside the native bridge package.",
              },
            ],
            patterns: [
              {
                group: ["@tauri-apps/api/*", "@tauri-apps/plugin-*"],
                message: "Import Tauri APIs only inside the native bridge package.",
              },
            ],
          },
        ],
      },
    },
  ];
}

function allowOwnerDependency({ from, to, source, targetFile = architectureEntrypoints.INDEX }) {
  const dependencySelector = {
    to: {
      element: {
        types: to,
        fileInternalPath: targetFile,
      },
    },
  };
  if (source !== undefined) {
    dependencySelector.dependency = { source };
  }
  return Object.freeze({
    from: {
      element: {
        types: from,
      },
    },
    allow: dependencySelector,
  });
}
