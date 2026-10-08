package xyz.vpavlin.shrooms

import androidx.compose.ui.text.LinkAnnotation
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** Code in a reply copies itself when tapped, as in Basecamp (activateLink). */
class CopyCodeTest {
    @Test fun inlineCodeIsALinkThatCopiesIt() {
        val copied = mutableListOf<String>()
        val text = spans(Markdown.inline("Run `ssh-keygen -t ed25519` in `~/x`, see https://example.org"), onCopy = { copied += it })
        val links = text.getLinkAnnotations(0, text.length)
        val copies = links.map { it.item }.filterIsInstance<LinkAnnotation.Clickable>()
        assertEquals(2, copies.size)
        copies.forEach { it.linkInteractionListener?.onClick(it) }
        assertEquals(listOf("ssh-keygen -t ed25519", "~/x"), copied)
        // A web link still opens; and without onCopy, code is plain text.
        assertTrue(links.any { it.item is LinkAnnotation.Url })
        assertTrue(spans(Markdown.inline("`a`")).getLinkAnnotations(0, 3).isEmpty())
    }
}
