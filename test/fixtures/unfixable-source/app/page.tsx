export default function Page() {
  // Deliberate type error in application source. Spool only edits generated
  // manifests, so this is expected to remain unfixable and be reported clearly.
  const answer: number = 'definitely not a number';
  return <main>{answer}</main>;
}