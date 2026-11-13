import js from "@eslint/js";
import jsxA11y from "eslint-plugin-jsx-a11y";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist", "node_modules", "src/api/generated"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  // Type-aware rules: they catch dropped promises, `any` leaking out of JSON and conditions that
  // can never change, which the plain rules cannot see.
  ...tseslint.configs.recommendedTypeChecked.map((c) => ({ ...c, files: ["src/**/*.{ts,tsx}"] })),
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      parserOptions: { projectService: true, tsconfigRootDir: import.meta.dirname },
    },
    rules: {
      "@typescript-eslint/no-deprecated": "error",
      "@typescript-eslint/no-unnecessary-condition": "error",
      "@typescript-eslint/no-unnecessary-type-assertion": "error",
      "@typescript-eslint/switch-exhaustiveness-check": "error",
    },
  },
  {
    // Mock handlers are async without awaiting, and JSON read back from a request is `any`.
    files: ["**/*.test.{ts,tsx}"],
    rules: {
      "@typescript-eslint/require-await": "off",
      "@typescript-eslint/no-unsafe-assignment": "off",
      "@typescript-eslint/no-unsafe-call": "off",
      "@typescript-eslint/no-unsafe-member-access": "off",
    },
  },
  reactHooks.configs.flat.recommended,
  jsxA11y.flatConfigs.recommended,
  {
    files: ["**/*.{ts,tsx}", "public/**/*.js"],
    languageOptions: { globals: globals.browser },
  },
  {
    // The design system (src/components/ui): pages compose kit components and use tokens.
    files: ["src/**/*.tsx"],
    ignores: ["src/components/ui/**", "**/*.test.tsx"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "JSXOpeningElement[name.name=/^(button|input|select|textarea|table|dialog)$/]",
          message:
            "Use the component from src/components/ui (Button, Input, Select, Textarea, Table, ConfirmDialog). If the kit lacks one, add it there.",
        },
        {
          selector:
            "Literal[value=/(^|[\\s:])(bg|text|border|fill|stroke|outline|ring|shadow|decoration|accent|caret|from|via|to)-\\[(#|rgba?\\(|hsla?\\(|oklch\\(|oklab\\()/]",
          message: "Use a design token (bg-muted, text-down, ...), not an arbitrary colour.",
        },
        {
          selector:
            "JSXAttribute[name.name='style'] Literal[value=/(#[0-9a-fA-F]{3,8}\\b|rgba?\\(|hsla?\\(|oklch\\(|oklab\\()/]",
          message: "Use a design token (var(--muted), ...), not a colour value in style.",
        },
        {
          selector:
            "TemplateElement[value.raw=/(^|[\\s:])(bg|text|border|fill|stroke|outline|ring|shadow|decoration|accent|caret|from|via|to)-\\[(#|rgba?\\(|hsla?\\(|oklch\\(|oklab\\()/]",
          message: "Use a design token (bg-muted, text-down, ...), not an arbitrary colour.",
        },
      ],
    },
  },
  {
    // The mock drops fields from its records with rest destructuring.
    files: ["mock/**/*.ts"],
    languageOptions: { globals: globals.node },
    rules: { "@typescript-eslint/no-unused-vars": ["error", { ignoreRestSiblings: true }] },
  },
  {
    files: ["vite.config.ts", "eslint.config.js"],
    languageOptions: { globals: globals.node },
  },
);
